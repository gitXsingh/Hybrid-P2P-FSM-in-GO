// Client: same CLI protocol as client.cpp (tracker + peer file transfer).
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	bufLen        = 1024
	fileChunkSize = 8192
	clientPort    = 9000
)

var (
	trackerConn   net.Conn
	loggedIn      bool
	currentUser   string
	downloadMu    sync.Mutex
	downloads     = map[string]*downloadInfo{}
	serverRunning bool
	peerLn        net.Listener
	peerServeDone sync.WaitGroup
)

type downloadInfo struct {
	filename       string
	groupID        string
	sourcePeer     string
	isCompleted    bool
	totalSize      int
	downloadedSize int
}

func sendToTrackerAndGetResponse(msg string) string {
	if trackerConn == nil {
		return ""
	}
	if _, err := trackerConn.Write([]byte(msg)); err != nil {
		return ""
	}
	buf := make([]byte, bufLen)
	n, err := trackerConn.Read(buf)
	out := string(buf[:n])
	if err != nil && err != io.EOF && n == 0 {
		return ""
	}
	return out
}

func sendToTracker(msg string) {
	resp := sendToTrackerAndGetResponse(msg)
	if strings.HasPrefix(msg, "login ") && resp == "Login successful" {
		parts := strings.Fields(msg)
		if len(parts) >= 2 {
			currentUser = parts[1]
			loggedIn = true
		}
		reg := sendToTrackerAndGetResponse(fmt.Sprintf("register_peer %d", clientPort))
		fmt.Println("Peer registration:", reg)
	} else if msg == "logout" && resp == "Logged out successfully" {
		loggedIn = false
		currentUser = ""
	}
	fmt.Println("Tracker:", resp)
}

func sendFile(filePath string, peer net.Conn) error {
	f, err := os.Open(filePath)
	if err != nil {
		base := filePath[strings.LastIndexAny(filePath, `/\`)+1:]
		f, err = os.Open(base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
			return err
		}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	fileSize := int(st.Size())
	if _, err := peer.Write([]byte(strconv.Itoa(fileSize))); err != nil {
		return err
	}
	ack := make([]byte, 16)
	_, _ = io.ReadFull(peer, ack[:3])

	buf := make([]byte, fileChunkSize)
	totalSent := 0
	progress := 0
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := peer.Write(buf[:n]); werr != nil {
				return werr
			}
			totalSent += n
			if fileSize > 0 {
				newP := (totalSent * 100) / fileSize
				if newP >= progress+10 {
					progress = newP
					fmt.Printf("Upload progress: %d%%\n", progress)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	fmt.Println("Upload complete")
	return nil
}

func receiveFile(savePath string, peer net.Conn, filename, groupID string) bool {
	buf := make([]byte, bufLen)
	n, err := peer.Read(buf)
	if n <= 0 || err != nil {
		fmt.Fprintln(os.Stderr, "Error receiving file size")
		return false
	}
	fileSize, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error parsing file size")
		return false
	}
	if _, err := peer.Write([]byte("ACK")); err != nil {
		return false
	}
	out, err := os.Create(savePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
		return false
	}
	defer out.Close()

	downloadMu.Lock()
	if downloads[filename] != nil {
		downloads[filename].totalSize = fileSize
	}
	downloadMu.Unlock()

	chunk := make([]byte, fileChunkSize)
	totalReceived := 0
	progress := 0
	for totalReceived < fileSize {
		need := fileChunkSize
		if fileSize-totalReceived < need {
			need = fileSize - totalReceived
		}
		if _, err := io.ReadFull(peer, chunk[:need]); err != nil {
			fmt.Fprintln(os.Stderr, "Error receiving file data")
			return false
		}
		if _, err := out.Write(chunk[:need]); err != nil {
			return false
		}
		totalReceived += need
		downloadMu.Lock()
		if d := downloads[filename]; d != nil {
			d.downloadedSize = totalReceived
		}
		downloadMu.Unlock()
		if fileSize > 0 {
			newP := (totalReceived * 100) / fileSize
			if newP >= progress+10 {
				progress = newP
				fmt.Printf("Download progress: %d%%\n", progress)
			}
		}
	}
	downloadMu.Lock()
	if d := downloads[filename]; d != nil {
		d.isCompleted = true
	}
	downloadMu.Unlock()
	fmt.Println("Download complete")

	notify := fmt.Sprintf("download_complete %s %s", groupID, filename)
	fmt.Println("Download notification:", sendToTrackerAndGetResponse(notify))
	return true
}

func downloadFile(groupID, filename, savePath string) {
	resp := sendToTrackerAndGetResponse(fmt.Sprintf("get_peer_info %s %s", groupID, filename))
	if strings.HasPrefix(resp, "ERROR") {
		fmt.Fprintln(os.Stderr, "Error:", resp)
		return
	}
	parts := strings.Fields(resp)
	if len(parts) < 2 {
		fmt.Fprintln(os.Stderr, "Invalid peer info format")
		return
	}
	peerUser := parts[0]
	ipPort := parts[1]
	colon := strings.LastIndex(ipPort, ":")
	if colon < 0 {
		fmt.Fprintln(os.Stderr, "Invalid peer info format")
		return
	}
	peerIP := ipPort[:colon]
	portNum, err := strconv.Atoi(ipPort[colon+1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid port number")
		return
	}
	fmt.Printf("Connecting to peer: %s at %s:%d\n", peerUser, peerIP, portNum)

	addr := net.JoinHostPort(peerIP, strconv.Itoa(portNum))
	peer, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Connection Failed")
		return
	}
	defer peer.Close()

	downloadMu.Lock()
	downloads[filename] = &downloadInfo{filename: filename, groupID: groupID, sourcePeer: peerUser}
	downloadMu.Unlock()

	req := fmt.Sprintf("download %s %s", groupID, filename)
	if _, err := peer.Write([]byte(req)); err != nil {
		return
	}
	if !receiveFile(savePath, peer, filename, groupID) {
		fmt.Fprintln(os.Stderr, "Download failed")
	}
}

func handlePeer(c net.Conn) {
	defer c.Close()
	buf := make([]byte, bufLen)
	n, err := c.Read(buf)
	if n <= 0 || err != nil {
		return
	}
	line := strings.TrimSpace(string(buf[:n]))
	parts := strings.Fields(line)
	if len(parts) < 3 || parts[0] != "download" {
		return
	}
	groupID, filename := parts[1], parts[2]
	fmt.Println("Received request from peer:", line)
	fp := sendToTrackerAndGetResponse(fmt.Sprintf("get_file_path %s %s", groupID, filename))
	if strings.HasPrefix(fp, "ERROR") {
		fmt.Fprintln(os.Stderr, "Error:", fp)
		return
	}
	fmt.Println("Sending file:", fp)
	_ = sendFile(fp, c)
}

func startPeerServer() {
	if serverRunning {
		fmt.Println("Peer server already running")
		return
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", clientPort))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bind failed: %v\n", err)
		return
	}
	peerLn = ln
	serverRunning = true
	peerServeDone.Add(1)
	go func() {
		defer peerServeDone.Done()
		fmt.Printf("Peer server started on port %d\n", clientPort)
		for {
			c, err := ln.Accept()
			if err != nil {
				if !serverRunning {
					return
				}
				continue
			}
			go handlePeer(c)
		}
	}()
}

func stopPeerServer() {
	if !serverRunning {
		return
	}
	serverRunning = false
	if peerLn != nil {
		_ = peerLn.Close()
	}
	_, _ = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(clientPort)))
	peerServeDone.Wait()
	peerLn = nil
	fmt.Println("Peer server stopped")
}

func displayHelp() {
	fmt.Println("Available commands:")
	fmt.Println("create_user <username> <password> - Create a new user")
	fmt.Println("login <username> <password> - Login with credentials")
	fmt.Println("create_group <group_id> - Create a new group")
	fmt.Println("join_group <group_id> - Request to join a group")
	fmt.Println("leave_group <group_id> - Leave a group")
	fmt.Println("list_requests <group_id> - List pending join requests for a group")
	fmt.Println("accept_request <group_id> <username> - Accept a user's join request")
	fmt.Println("list_groups - List all groups")
	fmt.Println("list_files <group_id> - List all files in a group")
	fmt.Println("upload_file <file_path> <group_id> - Share a file in a group")
	fmt.Println("download_file <group_id> <filename> <destination_path> - Download a file")
	fmt.Println("show_downloads - Show status of downloads")
	fmt.Println("stop_share <group_id> <filename> - Stop sharing a file")
	fmt.Println("logout - Logout from current session")
	fmt.Println("exit - Exit the application")
}

func showDownloads() {
	downloadMu.Lock()
	defer downloadMu.Unlock()
	if len(downloads) == 0 {
		fmt.Println("No downloads")
		return
	}
	fmt.Println("Downloads:")
	for _, info := range downloads {
		fmt.Printf("File: %s, Group: %s, Source: %s", info.filename, info.groupID, info.sourcePeer)
		if info.isCompleted {
			fmt.Println(" [COMPLETED]")
		} else {
			p := 0.0
			if info.totalSize > 0 {
				p = float64(info.downloadedSize) * 100 / float64(info.totalSize)
			}
			fmt.Printf(" [%g%%]\n", p)
		}
	}
}

func commandLoop() {
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !sc.Scan() {
			break
		}
		input := strings.TrimSpace(sc.Text())
		if input == "exit" || input == "quit" {
			break
		}
		if input == "help" {
			displayHelp()
			continue
		}
		if input == "show_downloads" {
			showDownloads()
			continue
		}
		if strings.HasPrefix(input, "upload_file ") {
			if !loggedIn {
				fmt.Println("Please login first")
				continue
			}
			parts := strings.Fields(input)
			if len(parts) < 3 {
				fmt.Println("Usage: upload_file <file_path> <group_id>")
				continue
			}
			fp := parts[1]
			groupID := parts[2]
			if _, err := os.Stat(fp); err != nil {
				fmt.Println("File not found:", fp)
				continue
			}
			sendToTracker(fmt.Sprintf("upload_file %s %s", fp, groupID))
			continue
		}
		if strings.HasPrefix(input, "download_file ") {
			if !loggedIn {
				fmt.Println("Please login first")
				continue
			}
			parts := strings.Fields(input)
			if len(parts) < 4 {
				fmt.Println("Usage: download_file <group_id> <filename> <destination_path>")
				continue
			}
			groupID, filename, dest := parts[1], parts[2], parts[3]
			go downloadFile(groupID, filename, dest)
			continue
		}
		sendToTracker(input)
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <tracker_ip:port>\n", os.Args[0])
		os.Exit(1)
	}
	host, portStr, err := net.SplitHostPort(os.Args[1])
	if err != nil {
		// allow host:port without brackets
		if idx := strings.LastIndex(os.Args[1], ":"); idx > 0 {
			host = os.Args[1][:idx]
			portStr = os.Args[1][idx+1:]
		} else {
			fmt.Fprintln(os.Stderr, "Invalid format. Use: <tracker_ip:port>")
			os.Exit(1)
		}
	}
	addr := net.JoinHostPort(host, portStr)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Connection Failed")
		os.Exit(1)
	}
	trackerConn = c
	fmt.Printf("Connected to tracker at %s\n", os.Args[1])

	startPeerServer()
	displayHelp()
	commandLoop()
	stopPeerServer()
	_ = trackerConn.Close()
}
