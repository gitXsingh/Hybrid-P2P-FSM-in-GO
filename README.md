# Hybrid Peer-to-Peer File Sharing System (Go)

This project is a Go implementation of a hybrid peer-to-peer file sharing system.

It uses a central tracker for coordination (users, groups, permissions, metadata, and peer discovery), while actual file data is transferred directly between peers. This design keeps the tracker lightweight and improves scalability as more clients become active.

## Features

- User authentication and account management
- Group-based access control for shared files
- Direct peer-to-peer file transfer
- Download progress tracking
- Concurrent client handling
- Command-line interface

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Components](#components)
- [Getting Started](#getting-started)
- [Available Commands](#available-commands)
- [How It Works](#how-it-works)

## Overview

The system provides controlled file sharing with centralized metadata and decentralized file transfer. It includes:

- **Tracker Server**: coordinates users, groups, file records, and peer availability
- **Client Application**: communicates with tracker and other peers for transfer operations

The implementation is written in Go and works well on Windows for local and LAN testing.

## Architecture

The architecture is hybrid: metadata is centralized, while file transfer is distributed.

```text
┌──────────────────┐                 ┌───────────────┐
│                  │                 │               │
│  Tracker Server  │◄────Control─────┤  Client Peer  │
│                  │─────Status─────►│               │
└────────┬─────────┘                 └───────┬───────┘
         │                                   │
         │                                   │
         │         ┌───────────────┐         │
         │         │               │         │
         └─────────┤  Client Peer  │◄────────┘
                   │               │    File Transfer
                   └───────────────┘   (Direct P2P)
```

## Components

### Tracker Server

The tracker manages metadata and access control only:

- Stores and validates user accounts
- Manages groups, memberships, and join requests
- Maintains shared file metadata
- Tracks currently online peers and their addresses
- Handles concurrent client sessions

**Key Data Structures:**
- `fileInfo`: file metadata including owner, path, size, and group visibility
- `group`: owner, members, pending requests, shared file names
- `peerInfo`: username, IP, and peer port

### Client Application

Each client handles both control-plane and data-plane roles:

- Talks to tracker for auth/group/metadata commands
- Runs a peer listener for incoming transfer requests
- Initiates direct peer-to-peer file transfer
- Provides command-line command loop
- Tracks active and completed downloads

**Key Data Structures:**
- `downloadInfo`: file/group/source/progress status
- Peer connection/session state for tracker and peer sockets

## Getting Started

### Prerequisites

- Go installed (recommended: Go 1.21+)
- Windows terminal (PowerShell or CMD)

### Build

From project root:

```powershell
go build -o tracker_go.exe ./cmd/tracker
go build -o client_go.exe ./cmd/client
```

Or use the helper script (if available):

```powershell
.\build-go.bat
```

### Start the Tracker

```powershell
.\tracker_go.exe dummy.txt 1
```

- The two arguments are accepted for compatibility.
- Tracker listens on port `8001` by default.

### Start a Client

```powershell
.\client_go.exe <tracker_ip:tracker_port>
```

Example (same machine):

```powershell
.\client_go.exe 127.0.0.1:8001
```

If the tracker is on another machine, use that LAN IP (for example `192.168.1.20:8001`).

On startup, the client:
- connects to the tracker
- starts a peer listener on port `9000`
- shows the available commands

## Available Commands

> Important: `<...>` are placeholders. Do **not** type angle brackets.

### User Management

```text
create_user <user_id> <password>
```
- Creates a new account.
- Example: `create_user john password123`

```text
login <user_id> <password>
```
- Authenticates the user.
- On success, the client automatically registers its peer endpoint with the tracker.

```text
logout
```
- Ends the current session and unregisters peer state.

### Group Management

```text
create_group <group_id>
```
- Creates a group and sets current user as owner.

```text
join_group <group_id>
```
- Sends a join request to the group owner.

```text
leave_group <group_id>
```
- Leaves group membership (owner cannot leave own group).

```text
list_requests <group_id>
```
- Owner-only: lists pending join requests.

```text
accept_request <group_id> <user_id>
```
- Owner-only: accepts a pending user request.

```text
list_groups
```
- Lists all groups and owners.

### File Operations

```text
list_files <group_id>
```
- Shows files visible in the group.

```text
upload_file <file_path> <group_id>
```
- Registers a file for sharing in the group.
- The file remains on the uploader peer; only metadata is stored on the tracker.

```text
download_file <group_id> <file_name> <destination_path>
```
- Gets peer info from the tracker, connects directly to the owner peer, and downloads the file to the destination path.

```text
show_downloads
```
- Shows current and completed downloads with progress.

```text
stop_share <group_id> <file_name>
```
- Stops sharing an owned file in that group.

### System Commands

```text
help
```
- Displays all available commands.

```text
exit
```
- Stops the client and peer listener, then exits.

## How It Works

### User Authentication Flow

```text
┌──────────┐                                 ┌─────────┐
│          │   1. create_user/login request  │         │
│  Client  ├─────────────────────────────────►         │
│          │                                 │ Tracker │
│          │   2. Authentication response    │         │
│          │◄────────────────────────────────┤         │
└──────────┘                                 └─────────┘
      │
      │ 3. If login is successful:
      │    - Start peer server
      │    - Register peer (IP:port)
      ▼
┌─────────────────┐
│                 │
│ Listening for   │
│ file requests   │
│                 │
└─────────────────┘
```

### File Transfer Process

1. **Request:** Client requests a file from a specific group.
2. **Verification:** Tracker checks group membership and file availability.
3. **Discovery:** Tracker provides IP and port of the peer with the file.
4. **Connection:** Requesting client connects directly to the file owner.
5. **Transfer:** File is sent in chunks with progress tracking.
6. **Completion:** Download state is updated at client and tracker.

```text
┌──────────┐    1. file request     ┌─────────┐
│          ├────────────────────────►         │
│ Client A │    2. peer info        │ Tracker │
│          │◄───────────────────────┤         │
└────┬─────┘                        └─────────┘
      │
      │ 3. direct connection
      ▼
┌──────────┐
│ Client B │
└────┬─────┘
      │ 4. file transfer
      └──────────────────────────────► Client A
```

### Group Management Process

```text
┌────────────┐                          ┌────────────┐
│            │  1. create_group         │            │
│  Owner     ├──────────────────────────►            │
│  Client    │                          │            │
└────────────┘                          │            │
                                        │  Tracker   │
┌────────────┐  2. join_group request   │            │
│            ├──────────────────────────►            │
│  User      │                          │            │
│  Client    │                          │            │
└────────────┘                          └────────────┘
                                               │
                 3. list_requests              │
┌────────────┐◄────────────────────────────────┘
│            │
│  Owner     │
│  Client    │  4. accept_request
│            ├────────────────────────────────►
└────────────┘                          ┌────────────┐
                                        │            │
                                        │  Tracker   │
                                        │            │
                                        └────────────┘
                                               │
                 5. group access granted       │
┌────────────┐◄────────────────────────────────┘
│            │
│  User      │
│  Client    │
│            │
└────────────┘
```

### Peer Server Operation

```text
┌─────────────────────────────────────────────────────┐
│                                                     │
│  Client Application                                 │
│                                                     │
│  ┌───────────────────┐      ┌────────────────────┐  │
│  │                   │      │                    │  │
│  │  Command Loop     │      │  Peer Server       │  │
│  │  (Main Thread)    │      │  (Background)      │  │
│  └─────────┬─────────┘      └──────────┬─────────┘  │
│            │                           │            │
│            ▼                           ▼            │
│  ┌───────────────────┐      ┌────────────────────┐  │
│  │  Tracker          │      │  File Request      │  │
│  │  Communication    │      │  Handler Routine   │  │
│  └───────────────────┘      └────────────────────┘  │
│                                                     │
└─────────────────────────────────────────────────────┘
```

### Group Management Rules

- Each group has a single owner who manages membership.
- Files can be shared with multiple groups.
- Users must join a group before accessing its files.
- Only group members can list/download group files.
- Group owners cannot leave their own groups.

---

## Conclusion

This project demonstrates a practical distributed design: centralized control with decentralized data transfer.

1. Reduced tracker load because file payloads bypass the tracker.
2. Better scalability through direct peer-to-peer transfer.
3. Clear access control using group-based permissions.
4. Efficient transfer paths with fewer network hops.

It is a solid foundation for future improvements such as checksum verification, resumable transfers, encryption, and richer peer discovery.
