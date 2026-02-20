# gRPC API Testing with grpcurl

grpcurl is a command-line tool for interacting with gRPC services without needing generated stubs.

## Installation

### macOS
```bash
brew install grpcurl
```

### Linux
```bash
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

### Docker
```bash
docker run --rm -it --network host fullstorydev/grpcurl:latest [commands]
```

## Basic Usage

### List Services
```bash
grpcurl -plaintext localhost:PORT list
grpcurl -plaintext localhost:PORT list package.Service
```

### Describe Service or Message Type
```bash
# List all methods in a service
grpcurl -plaintext localhost:PORT describe package.Service

# Show message structure
grpcurl -plaintext localhost:PORT describe package.MessageType
```

### Make RPC Calls

**Unary RPC (request/response)**
```bash
grpcurl -plaintext localhost:PORT package.Service/Method
grpcurl -plaintext -d '{"field": "value"}' localhost:PORT package.Service/Method
```

**Server Streaming**
```bash
grpcurl -plaintext localhost:PORT package.Service/StreamMethod
```

**Client Streaming** (send multiple messages)
```bash
grpcurl -plaintext localhost:PORT package.Service/ClientStreamMethod <<EOF
{"message": "first"}
{"message": "second"}
EOF
```

## Common Flags

| Flag | Purpose |
|------|---------|
| `-plaintext` | Use unencrypted connection (no TLS) |
| `-d '...'` | Send JSON request data |
| `-d @file.json` | Send request data from file |
| `-import-path PATH` | Add directory to proto import path |
| `-proto FILE` | Specify proto file to use |
| `-rpc-header NAME:VALUE` | Add custom HTTP header |
| `-metadata NAME:VALUE` | Add gRPC metadata |
| `-max-msg-sz SIZE` | Maximum message size in bytes |

## Using Proto Files for Better Reflection

```bash
grpcurl -plaintext \
  -import-path ./protos \
  -proto api.proto \
  -proto messages.proto \
  localhost:PORT package.Service/Method
```

## Working with Request Data

### Inline JSON
```bash
grpcurl -plaintext -d '{"id": 123, "name": "test"}' localhost:PORT pkg.Service/Method
```

### From File
```bash
grpcurl -plaintext -d @request.json localhost:PORT pkg.Service/Method
```

### Pretty-printed Output
```bash
grpcurl -plaintext -d '{}' localhost:PORT pkg.Service/Method | jq
```

## Common Patterns

### Exploratory Calls (No TLS, Development)
```bash
# Find what's available
grpcurl -plaintext localhost:50051 list

# Explore a service
grpcurl -plaintext localhost:50051 describe MyService

# See message format
grpcurl -plaintext localhost:50051 describe MyRequest
```

### Production Calls (With TLS, Authentication)
```bash
# TLS certificate verification
grpcurl -cacert ca.crt localhost:50051 list

# Client certificate
grpcurl -cacert ca.crt -cert client.crt -key client.key localhost:50051 list

# Custom headers/metadata
grpcurl -rpc-header "Authorization: Bearer TOKEN" localhost:50051 list
```

## Debugging

### Verbose Output
```bash
grpcurl -v -plaintext localhost:PORT pkg.Service/Method
```

### Check Service Health
```bash
grpcurl -plaintext localhost:PORT grpc.health.v1.Health/Check
```

### JSON Response Parsing
```bash
grpcurl -plaintext -d '{}' localhost:PORT pkg.Service/Method | jq '.field'
```

## Tips

- Always use `-plaintext` for local development servers
- Use `describe` to understand message structures before making calls
- Combine with `jq` for JSON processing: `grpcurl ... | jq`
- Proto files enable better autocompletion and validation
- Use `-d @file.json` for complex or repeated requests
