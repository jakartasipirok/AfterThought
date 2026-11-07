package execution

import "github.com/jakartasipirok/AfterThought/internal/graph"

// This adapter has no domain logic. Its bytes are part of every package identity.
// Node code and fitted models are loaded only inside the isolated container.
const pythonWrapper = `import contextlib
import hashlib
import importlib.util
import json
import os
import re
import sys

VERSION = "v2"
MAX_ARTIFACT = 64 * 1024 * 1024

def read_json(path, limit):
    with open(path, "rb") as f:
        data = f.read(limit + 1)
    if len(data) > limit:
        raise ValueError("payload exceeds limit")
    return data, json.loads(data)

_, request = read_json("/input/request.json", 1024 * 1024)
if request.get("version") != VERSION or not re.fullmatch(r"sha256:[a-f0-9]{64}", request.get("invocation_id", "")):
    raise ValueError("invalid request protocol")
inputs = {}
for port, ref in request["inputs"].items():
    digest, size = ref["digest"], ref["size"]
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest) or not isinstance(size, int) or not 0 <= size <= MAX_ARTIFACT:
        raise ValueError("invalid artifact reference")
    data, value = read_json("/input/" + digest[7:], MAX_ARTIFACT)
    if len(data) != size or "sha256:" + hashlib.sha256(data).hexdigest() != digest:
        raise ValueError("artifact checksum mismatch")
    inputs[port] = value
spec = importlib.util.spec_from_file_location("afterthought_node", "/package/source.py")
module = importlib.util.module_from_spec(spec)
# Agent-authored diagnostics must not corrupt the bounded result transport.
# Native writes to fd 1 remain untrusted and are rejected by the supervisor.
with contextlib.redirect_stdout(sys.stderr):
    spec.loader.exec_module(module)
    output = module.run(inputs, request["config"])
encoded = json.dumps(output, allow_nan=False, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("utf-8")
if len(encoded) > MAX_ARTIFACT:
    raise ValueError("output exceeds artifact limit")
with open("/output/output.json", "xb") as f:
    f.write(encoded)
    f.flush()
    os.fsync(f.fileno())
result = {"version": VERSION, "invocation_id": request["invocation_id"], "output": {"digest": "sha256:" + hashlib.sha256(encoded).hexdigest(), "size": len(encoded)}}
with open("/output/result.json", "x", encoding="utf-8") as f:
    json.dump(result, f, sort_keys=True, separators=(",", ":"))
    f.flush()
    os.fsync(f.fileno())
# /output is an 80 MiB container tmpfs, never a writable host bind mount.
# The host receives one bounded frame and independently validates it before
# committing any artifact. Files in the tmpfs disappear with the container.
header = json.dumps(result, sort_keys=True, separators=(",", ":")).encode("ascii")
wire = sys.stdout.buffer
wire.write(b"ATGRAPH2\n" + str(len(header)).encode("ascii") + b"\n")
wire.write(header)
wire.write(encoded)
wire.flush()
`

func wrapperArtifact() graph.Artifact {
	return graph.Artifact{Digest: graph.Hash([]byte(pythonWrapper)), Size: int64(len(pythonWrapper))}
}
