#!/usr/bin/env python3
"""The spike's generation test: can an agent write a wrapper from a spec,
with the conformance suite as its test?  (SPIKE.md; doc 35 §8, §19.)

For one tool, blind -- the agent never sees the hand-written wrapper, a key,
or the web:

  1. A fresh workspace holds the contract (the vendored wrapperprotocol), the
     authoring guide (this repository's README, with every line naming a
     hand-written wrapper removed), and the tool's documentation, fetched and
     fed in (spec/<tool>/).
  2. Mendel's own code-generation executor (internal/codegen/executor, on
     claude-sonnet-5, with its own system prompt and tools) writes the
     wrapper and its unit tests, driven by gendriver (driver/main.go.txt,
     built from a Mendel checkout). It runs in a container that holds the
     workspace and nothing else, as a non-root user; the Anthropic key
     reaches the driver on stdin and is never in the environment or on disk,
     because the executor's Bash tool runs with the driver's environment.
  3. This script builds it and runs the conformance suite against the real
     venue, with the key given only to the suite. Whatever did not pass goes
     back as a new run on the same workspace, for at most three repair
     rounds -- as Mendel's own code generation does it (repairPrompt,
     maxTestRepairs).

Every round's spend, turns and time, and the suite's counts, are written to
results/<tool>-<stamp>.json. Offline tooling: never on Mendel's serving path.

    python3 spike/generate/generate.py plausible   # needs venues/plausible-ce up
    python3 spike/generate/generate.py tavily      # spends Tavily credits: 2 per suite run
"""
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
MODEL = "claude-sonnet-5"
MAX_REPAIRS = 3
BUDGET_PER_CALL_USD = "3"
DRIVER = Path(os.environ.get("SPIKE_DRIVER", "/tmp/mendel-spike-generate/gendriver"))
IMAGE = "golang:1.26"
CONFIG_DIR = Path.home() / "Library" / "Application Support" / "mendel-conformance"
# Workspaces live outside this repository, so the hand-written wrappers are
# not beside the agent even as parent directories.
WORK = Path(os.environ.get("SPIKE_WORK", "/tmp/mendel-spike-generate"))

TOOLS = {
    "plausible": {
        "name": "Plausible Analytics", "homepage": "https://plausible.io", "contract": "1",
        "role": "a read-only data source: Mendel reads numbers from it (probe, read_total and read_series, "
                "and every other verb declared absent with its reason)",
        "account": "conformance.test", "endpoint": "http://localhost:8765", "at": "2026-09-29T06:00:00Z",
        "expect": REPO / "venues/plausible-ce/.work/expected.json",
        "keyfile": REPO / "venues/plausible-ce/.work/venue.env", "keyvar": "PLAUSIBLE_API_KEY",
        "venue": "a self-hosted Plausible Community Edition, reached through the connection's endpoint",
    },
    "tavily": {
        "name": "Tavily", "homepage": "https://tavily.com", "contract": "2-draft",
        "role": "a search tool: Mendel searches with it (probe and search, and every other verb declared "
                "absent with its reason)",
        "account": None, "endpoint": None, "at": None, "expect": None,
        "keyfile": CONFIG_DIR / "tavily.env", "keyvar": "TAVILY_API_KEY",
        "venue": "Tavily's hosted API, on an account whose every search spends credits",
    },
}

PROMPT = """You are writing an External Tool Wrapper for {name} ({homepage}), for Mendel.

Read GUIDE.md first: what a wrapper is, the wire protocol, wrapper.json, and what
Mendel checks. The protocol's Go types are the source of truth, in
vendor/github.com/mendelbuild/mendelbuild/wrapperprotocol/ (protocol.go, draft.go,
description.go); import that package rather than restating it. {name}'s own API
documentation, fetched today, is in spec/{slug}/. Write against it, not against
what you remember of the API, and cite what you used, with the date, in
{slug}/README.md.

Write the wrapper in {slug}/ as package main. It is {role}. It speaks contract
"{contract}". It will be run against {venue}.

- {slug}/wrapper.json: tool.slug "{slug}", version "0.1.0", contract "{contract}",
  image "mendel-tool-{slug}:dev", a command equal to the Dockerfile's ENTRYPOINT,
  the connection a person supplies (the API key as a credential), and your claims.
- {slug}/Dockerfile, as GUIDE.md describes.
- Standard library and the wrapperprotocol package only. It must build with
  `go build -mod=vendor ./{slug}`.
- Unit tests against a fake server (net/http/httptest). Check your work: run
  `go build ./{slug}` and `go test ./{slug}` and make both pass (the module is
  vendored; GOFLAGS already says so). Never call the real API from a test, and
  do not try to reach it at all: there is no key here.

When you finish, Mendel builds the wrapper and runs its conformance suite
against a real {name} account. If anything does not pass, you will be told
what, and asked to fix it."""

REPAIR = """You are fixing an External Tool Wrapper for {name}, for Mendel, in {slug}/.
The workspace holds your earlier work; GUIDE.md, the protocol in
vendor/github.com/mendelbuild/mendelbuild/wrapperprotocol/ and the tool's
documentation in spec/{slug}/ are as before.

Mendel built the wrapper and ran the conformance suite against a real {name}
account. {summary}

What did not pass:

{failures}

Fix the wrapper so these pass, keeping everything else passing. If a check
shows the wrapper.json and the wrapper disagree, change whichever is wrong.
The unit tests should still pass."""


def workspace(slug: str) -> Path:
    stamp = time.strftime("%Y%m%d-%H%M%S")
    ws = WORK / f"{slug}-{stamp}"
    ws.mkdir(parents=True)
    for f in ("go.mod", "go.sum"):
        shutil.copy(REPO / f, ws / f)
    shutil.copytree(REPO / "vendor", ws / "vendor")
    shutil.copytree(HERE / "spec" / slug, ws / "spec" / slug)
    # The authoring guide, blind: no line that names a hand-written wrapper.
    names = [t for t in TOOLS] + ["mastodon", "Mastodon"]
    guide = [l for l in (REPO / "README.md").read_text().splitlines()
             if not any(n.lower() in l.lower() for n in names)]
    (ws / "GUIDE.md").write_text("\n".join(guide) + "\n")
    subprocess.run(["git", "init", "-q"], cwd=ws, check=True)
    return ws


def agent(ws: Path, prompt: str, rnd: int) -> dict:
    """One run of Mendel's executor in a container holding only the workspace."""
    task = ws.parent / f"{ws.name}.prompt-{rnd}.txt"
    task.write_text(prompt)
    anthropic = key({"keyfile": CONFIG_DIR / "anthropic.env", "keyvar": "ANTHROPIC_API_KEY"})
    cmd = ["docker", "run", "-i", "--rm", "--user", f"{os.getuid()}:{os.getgid()}",
           "-v", f"{ws}:/work", "-v", f"{DRIVER}:/usr/local/bin/gendriver:ro", "-v", f"{task}:/prompt.txt:ro",
           "-w", "/work", "-e", "HOME=/tmp", "-e", "GOCACHE=/tmp/go-cache", "-e", "GOFLAGS=-mod=vendor",
           "-e", "GOTOOLCHAIN=local", IMAGE,
           "gendriver", "-dir", "/work", "-prompt", "/prompt.txt", "-limit", BUDGET_PER_CALL_USD,
           "-out", f"/work/.gendriver-round-{rnd}.json"]
    start = time.time()
    p = subprocess.run(cmd, input=f"ANTHROPIC_API_KEY={anthropic}\n", capture_output=True, text=True, timeout=3600)
    (ws.parent / f"{ws.name}.agent-{rnd}.log").write_text(p.stderr)
    try:
        out = json.loads(p.stdout)
    except json.JSONDecodeError:
        out = {"error": (p.stdout + p.stderr)[-2000:]}
    out["wall_seconds"] = round(time.time() - start, 1)
    return out


def key(cfg: dict) -> str:
    for line in Path(cfg["keyfile"]).read_text().splitlines():
        name, _, value = line.partition("=")
        if name == cfg["keyvar"]:
            return value.strip()
    raise SystemExit(f"{cfg['keyvar']} is not in {cfg['keyfile']}")


def suite(ws: Path, slug: str, cfg: dict) -> dict:
    """Build the wrapper and run the conformance suite; the key reaches only the suite."""
    b = subprocess.run(["go", "build", "-mod=vendor", "-o", "bin/wrapper", f"./{slug}"], cwd=ws,
                       capture_output=True, text=True)
    if b.returncode != 0:
        return {"built": False, "error": b.stderr[-3000:]}
    try:
        desc = json.loads((ws / slug / "wrapper.json").read_text())
        names = [c["name"] for c in desc.get("connection", {}).get("credentials", [])]
    except Exception as e:  # the suite would refuse it too; say why here
        return {"built": True, "error": f"{slug}/wrapper.json is not readable: {e}"}
    env = dict(os.environ)
    for n in names:  # whatever the agent named the key, the suite supplies it
        env[n] = key(cfg)
    harness = WORK / "conformance"
    args = [str(harness), "-file", f"{slug}/wrapper.json", "-cmd", "bin/wrapper", "-json", "report.json"]
    if cfg["account"]:
        args += ["-account", cfg["account"]]
    if cfg["endpoint"]:
        args += ["-endpoint", cfg["endpoint"]]
    if cfg["at"]:
        args += ["-at", cfg["at"]]
    if cfg["expect"]:
        args += ["-expect", str(cfg["expect"])]
    r = subprocess.run(args, cwd=ws, env=env, capture_output=True, text=True, timeout=600)
    if not (ws / "report.json").exists():
        return {"built": True, "error": (r.stdout + r.stderr)[-3000:]}
    report = json.loads((ws / "report.json").read_text())
    counts = {}
    for c in report["checks"]:
        counts[c["outcome"]] = counts.get(c["outcome"], 0) + 1
    failing = [c for c in report["checks"] if c["outcome"] in ("fail", "untested")]
    return {"built": True, "counts": counts, "passed": not failing, "text": r.stdout,
            "failures": [f"- {c['outcome'].upper()} {c.get('verb', '-')}: {c['name']}: {c.get('detail', '')}" for c in failing]}


def main():
    slug = sys.argv[1]
    cfg = TOOLS[slug]
    harness = WORK / "conformance"
    harness.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["go", "build", "-mod=vendor", "-o", str(harness), "./cmd/conformance"], cwd=REPO, check=True)
    ws = workspace(slug)
    record = {"tool": slug, "model": MODEL, "agent": "mendel internal/codegen/executor via gendriver",
              "workspace": str(ws), "rounds": []}
    prompt = PROMPT.format(slug=slug, **cfg)
    for rnd in range(MAX_REPAIRS + 1):
        out = agent(ws, prompt, rnd)
        if not out.get("spend_usd") and out.get("error"):
            # The agent never ran (a key, a flag): not a round of anything.
            raise SystemExit(f"the agent did not start: {out.get('error')}")
        res = suite(ws, slug, cfg)
        row = {"round": rnd, "cost_usd": round(out.get("spend_usd") or 0, 4), "api_rounds": out.get("api_rounds"),
               "tool_calls": out.get("tool_calls"), "wall_seconds": out.get("wall_seconds"),
               "agent_error": out.get("error"), "stopped_for_budget": out.get("stopped_for_budget"),
               "built": res.get("built"), "counts": res.get("counts"), "passed": res.get("passed", False)}
        record["rounds"].append(row)
        print(json.dumps(row), flush=True)
        if res.get("passed"):
            break
        if "error" in res:
            summary, failures = "It did not get as far as the suite.", res["error"]
        else:
            summary = f"{res['counts'].get('pass', 0)} checks passed."
            failures = "\n".join(res["failures"])
        prompt = REPAIR.format(name=cfg["name"], slug=slug, summary=summary, failures=failures)
    record["passed"] = record["rounds"][-1]["passed"]
    record["repair_rounds"] = len(record["rounds"]) - 1
    record["cost_usd"] = round(sum(r["cost_usd"] or 0 for r in record["rounds"]), 4)
    (ws / "suite-output.txt").write_text(res.get("text") or res.get("error", ""))
    out_dir = HERE / "results"
    out_dir.mkdir(exist_ok=True)
    out = out_dir / f"{ws.name}.json"
    out.write_text(json.dumps(record, indent=2) + "\n")
    print(json.dumps({k: record[k] for k in ("tool", "passed", "repair_rounds", "cost_usd")}), flush=True)
    print(f"record: {out}", flush=True)


if __name__ == "__main__":
    main()
