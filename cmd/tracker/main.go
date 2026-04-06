// Tracker server: TCP command protocol (same behavior as tracker.cpp).
package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const port = 8001
const bufLen = 1024

type fileInfo struct {
	filename string
	owner    string
	filepath string
	size     int
	groups   map[string]struct{}
}

type group struct {
	groupID          string
	owner            string
	members          map[string]struct{}
	pendingRequests  map[string]struct{}
	files            map[string]struct{}
}

type peerInfo struct {
	username string
	ip       string
	port     int
}

type server struct {
	mu          sync.RWMutex
	users       map[string]string
	userFiles   map[string][]fileInfo
	loggedIn    map[uint64]string
	socketToIP  map[uint64]string
	groups      map[string]*group
	downloads   map[string]map[string]bool
	peerInfo    map[string]peerInfo
	nextConnID  uint64
}

func newServer() *server {
	return &server{
		users:      make(map[string]string),
		userFiles:  make(map[string][]fileInfo),
		loggedIn:   make(map[uint64]string),
		socketToIP: make(map[uint64]string),
		groups:     make(map[string]*group),
		downloads:  make(map[string]map[string]bool),
		peerInfo:   make(map[string]peerInfo),
	}
}

func parseCommand(input string) (cmd string, args []string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", nil
	}
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

func (s *server) isGroupOwner(username, groupID string) bool {
	g, ok := s.groups[groupID]
	if !ok {
		return false
	}
	return g.owner == username
}

func (s *server) isGroupMember(username, groupID string) bool {
	g, ok := s.groups[groupID]
	if !ok {
		return false
	}
	_, m := g.members[username]
	return m
}

func (s *server) findFileOwner(filename, groupID string) string {
	for _, files := range s.userFiles {
		for _, f := range files {
			if f.filename == filename {
				if _, ok := f.groups[groupID]; ok {
					return f.owner
				}
			}
		}
	}
	return ""
}

func (s *server) getFilePath(username, filename, groupID string) string {
	for _, f := range s.userFiles[username] {
		if f.filename == filename {
			if _, ok := f.groups[groupID]; ok {
				return f.filepath
			}
		}
	}
	return ""
}

func (s *server) handleLine(connID uint64, clientIP, line string) (response string, disconnect bool) {
	cmd, args := parseCommand(line)
	switch cmd {
	case "create_user":
		if len(args) < 2 {
			return "ERROR: Usage: create_user <username> <password>", false
		}
		u, p := args[0], args[1]
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.users[u]; ok {
			return "ERROR: User already exists", false
		}
		s.users[u] = p
		return "User created successfully", false

	case "login":
		if len(args) < 2 {
			return "ERROR: Usage: login <username> <password>", false
		}
		u, p := args[0], args[1]
		s.mu.Lock()
		defer s.mu.Unlock()
		if pw, ok := s.users[u]; !ok {
			return "ERROR: User does not exist", false
		} else if pw != p {
			return "ERROR: Invalid password", false
		}
		s.loggedIn[connID] = u
		return "Login successful", false

	case "register_peer":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 1 {
			return "ERROR: Usage: register_peer <port>", false
		}
		portNum, err := strconv.Atoi(args[0])
		if err != nil {
			return "ERROR: Invalid port number", false
		}
		username := s.loggedIn[connID]
		ip := s.socketToIP[connID]
		s.peerInfo[username] = peerInfo{username: username, ip: ip, port: portNum}
		return "Peer registered successfully", false

	case "get_peer_info":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 2 {
			return "ERROR: Usage: get_peer_info <group_id> <filename>", false
		}
		username := s.loggedIn[connID]
		groupID, filename := args[0], args[1]
		_, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupMember(username, groupID) {
			return "ERROR: Not a member of the group", false
		}
		owner := s.findFileOwner(filename, groupID)
		if owner == "" {
			return "ERROR: File not found in group", false
		}
		pi, ok := s.peerInfo[owner]
		if !ok {
			return "ERROR: File owner not currently online", false
		}
		return fmt.Sprintf("%s %s:%d", pi.username, pi.ip, pi.port), false

	case "get_file_path":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 2 {
			return "ERROR: Usage: get_file_path <group_id> <filename>", false
		}
		username := s.loggedIn[connID]
		groupID, filename := args[0], args[1]
		path := s.getFilePath(username, filename, groupID)
		if path == "" {
			return "ERROR: File not found", false
		}
		return path, false

	case "download_complete":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 2 {
			return "ERROR: Usage: download_complete <group_id> <filename>", false
		}
		username := s.loggedIn[connID]
		_, filename := args[0], args[1]
		if s.downloads[username] == nil {
			s.downloads[username] = make(map[string]bool)
		}
		s.downloads[username][filename] = true
		return "Download status updated", false

	case "create_group":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 1 {
			return "ERROR: Usage: create_group <group_id>", false
		}
		username := s.loggedIn[connID]
		groupID := args[0]
		if _, exists := s.groups[groupID]; exists {
			return "ERROR: Group already exists", false
		}
		s.groups[groupID] = &group{
			groupID:         groupID,
			owner:           username,
			members:         map[string]struct{}{username: {}},
			pendingRequests: make(map[string]struct{}),
			files:           make(map[string]struct{}),
		}
		return "Group created successfully", false

	case "join_group":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 1 {
			return "ERROR: Usage: join_group <group_id>", false
		}
		username := s.loggedIn[connID]
		groupID := args[0]
		g, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if s.isGroupMember(username, groupID) {
			return "ERROR: Already a member of the group", false
		}
		g.pendingRequests[username] = struct{}{}
		return "Join request sent", false

	case "leave_group":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 1 {
			return "ERROR: Usage: leave_group <group_id>", false
		}
		username := s.loggedIn[connID]
		groupID := args[0]
		g, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupMember(username, groupID) {
			return "ERROR: Not a member of the group", false
		}
		if s.isGroupOwner(username, groupID) {
			return "ERROR: Group owner cannot leave the group", false
		}
		delete(g.members, username)
		return "Left group successfully", false

	case "list_requests":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 1 {
			return "ERROR: Usage: list_requests <group_id>", false
		}
		username := s.loggedIn[connID]
		groupID := args[0]
		g, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupOwner(username, groupID) {
			return "ERROR: Only group owner can list requests", false
		}
		var b strings.Builder
		b.WriteString("Pending requests for group ")
		b.WriteString(groupID)
		b.WriteString(":\n")
		if len(g.pendingRequests) == 0 {
			b.WriteString("No pending requests")
		} else {
			for u := range g.pendingRequests {
				b.WriteString(u)
				b.WriteString("\n")
			}
		}
		return b.String(), false

	case "accept_request":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 2 {
			return "ERROR: Usage: accept_request <group_id> <user_id>", false
		}
		username := s.loggedIn[connID]
		groupID, target := args[0], args[1]
		g, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupOwner(username, groupID) {
			return "ERROR: Only group owner can accept requests", false
		}
		if _, ok := g.pendingRequests[target]; !ok {
			return "ERROR: No pending request from this user", false
		}
		delete(g.pendingRequests, target)
		g.members[target] = struct{}{}
		return "User accepted to group", false

	case "list_groups":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		var b strings.Builder
		b.WriteString("Available groups:\n")
		if len(s.groups) == 0 {
			b.WriteString("No groups available")
			return b.String(), false
		}
		for id, g := range s.groups {
			b.WriteString(id)
			b.WriteString(" - Owner: ")
			b.WriteString(g.owner)
			b.WriteString("\n")
		}
		return b.String(), false

	case "list_files":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 1 {
			return "ERROR: Usage: list_files <group_id>", false
		}
		username := s.loggedIn[connID]
		groupID := args[0]
		if _, ok := s.groups[groupID]; !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupMember(username, groupID) {
			return "ERROR: Not a member of the group", false
		}
		var b strings.Builder
		b.WriteString("Files in group ")
		b.WriteString(groupID)
		b.WriteString(":\n")
		has := false
		for _, files := range s.userFiles {
			for _, f := range files {
				if _, ok := f.groups[groupID]; ok {
					has = true
					fmt.Fprintf(&b, "%s (%d bytes) - Owner: %s\n", f.filename, f.size, f.owner)
				}
			}
		}
		if !has {
			b.WriteString("No files in this group")
		}
		return b.String(), false

	case "upload_file":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 2 {
			return "ERROR: Usage: upload_file <file_path> <group_id>", false
		}
		username := s.loggedIn[connID]
		fp := args[0]
		groupID := args[1]
		filename := filepath.Base(strings.ReplaceAll(fp, `\`, `/`))
		g, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupMember(username, groupID) {
			return "ERROR: Not a member of the group", false
		}
		size := 1024
		if st, err := os.Stat(fp); err == nil && !st.IsDir() {
			size = int(st.Size())
		}
		found := false
		files := s.userFiles[username]
		for i := range files {
			if files[i].filename == filename {
				if files[i].groups == nil {
					files[i].groups = make(map[string]struct{})
				}
				files[i].groups[groupID] = struct{}{}
				s.userFiles[username] = files
				found = true
				break
			}
		}
		if !found {
			gset := map[string]struct{}{groupID: {}}
			s.userFiles[username] = append(s.userFiles[username], fileInfo{
				filename: filename,
				owner:    username,
				filepath: fp,
				size:     size,
				groups:   gset,
			})
		}
		g.files[filename] = struct{}{}
		return "File uploaded successfully", false

	case "download_file":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 3 {
			return "ERROR: Usage: download_file <group_id> <file_name> <destination_path>", false
		}
		username := s.loggedIn[connID]
		groupID, filename := args[0], args[1]
		if _, ok := s.groups[groupID]; !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupMember(username, groupID) {
			return "ERROR: Not a member of the group", false
		}
		fileFound := false
		for _, files := range s.userFiles {
			for _, f := range files {
				if f.filename == filename {
					if _, ok := f.groups[groupID]; ok {
						fileFound = true
						break
					}
				}
			}
		}
		if !fileFound {
			return "ERROR: File not found in group", false
		}
		if s.downloads[username] == nil {
			s.downloads[username] = make(map[string]bool)
		}
		s.downloads[username][filename] = false
		return "Download request registered", false

	case "show_downloads":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		username := s.loggedIn[connID]
		ds, ok := s.downloads[username]
		if !ok || len(ds) == 0 {
			return "No downloads", false
		}
		var b strings.Builder
		b.WriteString("Downloads:\n")
		for fn, done := range ds {
			status := "[D]"
			if done {
				status = "[C]"
			}
			gid := "Unknown"
			for gid2, g := range s.groups {
				if _, ok := g.files[fn]; ok {
					gid = gid2
					break
				}
			}
			fmt.Fprintf(&b, "%s [%s] %s\n", status, gid, fn)
		}
		return b.String(), false

	case "stop_share":
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loggedIn[connID]; !ok {
			return "ERROR: Not logged in", false
		}
		if len(args) < 2 {
			return "ERROR: Usage: stop_share <group_id> <file_name>", false
		}
		username := s.loggedIn[connID]
		groupID, filename := args[0], args[1]
		g, ok := s.groups[groupID]
		if !ok {
			return "ERROR: Group does not exist", false
		}
		if !s.isGroupMember(username, groupID) {
			return "ERROR: Not a member of the group", false
		}
		files := s.userFiles[username]
		fileFound := false
		for i := range files {
			if files[i].filename != filename {
				continue
			}
			if _, ok := files[i].groups[groupID]; !ok {
				continue
			}
			delete(files[i].groups, groupID)
			fileFound = true
			s.userFiles[username] = files
			break
		}
		if !fileFound {
			return "ERROR: You don't own this file in this group", false
		}
		still := false
		for _, ufs := range s.userFiles {
			for _, f := range ufs {
				if f.filename == filename {
					if _, ok := f.groups[groupID]; ok {
						still = true
						break
					}
				}
			}
		}
		if !still {
			delete(g.files, filename)
		}
		return "File sharing stopped", false

	case "logout":
		s.mu.Lock()
		defer s.mu.Unlock()
		u, ok := s.loggedIn[connID]
		if !ok {
			return "ERROR: Not logged in", false
		}
		delete(s.peerInfo, u)
		delete(s.loggedIn, connID)
		return "Logged out successfully", false

	case "quit", "exit":
		return "Goodbye!", true

	default:
		return "ERROR: Unknown command", false
	}
}

func (s *server) cleanupConn(connID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.loggedIn[connID]; ok {
		delete(s.peerInfo, u)
		delete(s.loggedIn, connID)
	}
	delete(s.socketToIP, connID)
}

func handleConn(s *server, c net.Conn) {
	defer c.Close()
	connID := atomic.AddUint64(&s.nextConnID, 1)
	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	s.mu.Lock()
	s.socketToIP[connID] = host
	s.mu.Unlock()

	buf := make([]byte, bufLen)
	for {
		n, err := c.Read(buf)
		if n <= 0 {
			if err != io.EOF && err != nil {
				log.Printf("read: %v", err)
			}
			break
		}
		line := string(buf[:n])
		if idx := strings.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		resp, disconnect := s.handleLine(connID, host, line)
		if _, werr := c.Write([]byte(resp)); werr != nil {
			break
		}
		if disconnect {
			break
		}
	}
	s.cleanupConn(connID)
	log.Println("Client disconnected.")
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <tracker_info.txt> <tracker_no>\n", os.Args[0])
		os.Exit(1)
	}
	srv := newServer()
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Tracker started on port %d\n", port)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("Connection accepted")
		go handleConn(srv, c)
	}
}
