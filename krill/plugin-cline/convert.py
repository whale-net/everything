#!/usr/bin/env python3
"""One-off converter: krill Claude Code plugins -> Cline plugins."""
import json, os, re

SRC = "/home/alex/whale_net/everything/krill/plugin"
DST = "/home/alex/whale_net/everything-worktrees/krill-cline-plugins/krill/plugin-cline"

PERSONAS = ["system-validator", "quick-task", "stakeholder", "mergepush",
            "producer", "architect", "reviewer", "planner", "validator",
            "worker", "help"]
SKILLS = ["loop-design-panel", "loop-plan-implement-validate", "stakeholder-meeting",
          "design", "product", "review", "status", "plan", "implement", "validate"]
P = "|".join(PERSONAS)
S = "|".join(SKILLS)

def convert(text):
    # Persona dispatches (before skill rewrite)
    text = re.sub(r"[Dd]ispatches one `(?:krill-design|krill-work):(" + P + r")` subagent",
                  lambda m: "Spawns one subagent (Cline: new_task) with the `krill-" + m.group(1) + "` custom mode as its mode", text)
    text = re.sub(r"[Dd]ispatch `(?:krill-design|krill-work):(" + P + r")`",
                  lambda m: ("Spawn" if m.group(0)[0] == "D" else "spawn") + " a subagent (Cline: new_task) with the `krill-" + m.group(1) + "` custom mode as its mode", text)
    text = re.sub(r"`(?:krill-design|krill-work):(" + P + r")`",
                  r"the `krill-\1` custom mode", text)
    # Skill slash commands -> workflow wording
    text = re.sub(r"`?/?krill-(?:design|work):(" + S + r")`?", r"the `\1` workflow", text)
    # MCP tool names -> plain server/tool wording
    text = re.sub(r"mcp__plugin_krill-(?:design|work)_krill-mcp-([a-z-]+)__([a-z_]+)\b",
                  r"the \2 tool on the krill-mcp-\1 MCP server", text)
    text = re.sub(r"mcp__plugin_krill-(?:design|work)_krill-mcp-([a-z-]+)__\*",
                  r"the tools of the krill-mcp-\1 MCP server", text)
    # Agent file paths -> custom modes (project-manager refs stay)
    text = re.sub(r"krill/plugin/(design|work)/agents/(\w+)\.md",
                  r"the \2 custom mode in krill/plugin-cline/\1/.roomodes", text)
    text = re.sub(r"(?<!project-manager/)(?<![\w/-])agents/([\w-]+)\.md",
                  r"the \1 custom mode in .roomodes", text)
    # Skill paths -> workflows
    text = re.sub(r"skills/([a-z-]+)/SKILL\.md", r"workflows/\1.md", text)
    # Shared locations
    text = re.sub(r"krill/plugin/shared/skills/([a-z-]+)/",
                  r"krill/plugin-cline/{design,work}/workflows/\1.md", text)
    text = re.sub(r"shared/agents/help\.md", r"the krill-help custom mode in .roomodes", text)
    text = re.sub(r"krill/plugin/shared/CONVENTIONS\.md",
                  r"krill/plugin-cline/shared/CONVENTIONS.md", text)
    text = re.sub(r"krill/plugin/", "krill/plugin-cline/", text)
    # Claude-isms
    text = re.sub(r"fresh `general-purpose` subagents?", "fresh subagents with the appropriate `krill-*` custom mode", text)
    text = text.replace("a fresh `general-purpose` subagent",
                        "a fresh subagent (Cline: new_task with the appropriate `krill-*` custom mode)")
    text = text.replace("`SendMessage`", "a `new_task` follow-up message")
    text = text.replace("SendMessage", "new_task")
    text = re.sub(r"Claude\s+Code", "Cline", text)
    text = re.sub(r"TaskCreate/TaskUpdate(/TaskList)?", "Cline's built-in task tracking", text)
    # Terminology
    text = re.sub(r"\bskills\b", "workflows", text)
    text = re.sub(r"\bskill\b", "workflow", text)
    text = re.sub(r"\bSlash command\b", "Workflow invocation", text)
    text = re.sub(r"\bslash command\b", "workflow invocation", text)
    return text

with open(os.path.join(SRC, "shared/snippets/task-lifecycle-blocker.md")) as f:
    SNIPPET = f.read().strip()
SNIPPET_SECTION = "## Task lifecycle blocker (from shared/snippets)\n\n" + SNIPPET.replace("Claude Code", "Cline") + "\n"

def strip_fm(path):
    with open(path) as f:
        raw = f.read()
    m = re.match(r"^---\n(.*?)\n---\n", raw, re.S)
    fm, body = {}, raw
    if m:
        for line in m.group(1).splitlines():
            if ":" in line:
                k, v = line.split(":", 1)
                fm[k.strip()] = v.strip()
        body = raw[m.end():]
    return fm, body

def apply_include(body):
    if re.search(r"^[ \t]*@.*task-lifecycle-blocker\.md", body, flags=re.M):
        body = re.sub(r"^[ \t]*@.*task-lifecycle-blocker\.md[ \t]*$", "", body, flags=re.M).rstrip()
        body += "\n\n" + SNIPPET_SECTION
    return body

def mcp_servers(src):
    with open(src) as f:
        data = json.load(f)["mcpServers"]
    out = {}
    for name, cfg in data.items():
        entry = {"type": "streamableHttp", "url": cfg["url"]}
        if "headers" in cfg:
            entry["headers"] = cfg["headers"]
        entry["alwaysAllow"] = []
        out[name] = entry
    return {"mcpServers": out}

def workflow_md(skilldir, fm, body):
    desc = convert(fm.get("description", ""))
    body = body.strip()
    if body.startswith("# "):
        nl = body.index("\n")
        return body[:nl] + "\n\n*" + desc + "*\n" + body[nl:] + "\n"
    return "# " + fm.get("name", skilldir).replace("-", " ").title() + "\n\n*" + desc + "*\n\n" + body + "\n"

os.makedirs(DST, exist_ok=True)
os.makedirs(os.path.join(DST, "shared"), exist_ok=True)
for plugin in ["design", "work"]:
    pdst = os.path.join(DST, plugin)
    os.makedirs(os.path.join(pdst, "workflows"), exist_ok=True)
    srv_suffix = plugin
    modes = []

    agent_files = [os.path.join(SRC, plugin, "agents", f)
                   for f in sorted(os.listdir(os.path.join(SRC, plugin, "agents")))]
    if "help.md" not in os.listdir(os.path.join(SRC, plugin, "agents")):
        agent_files.append(os.path.join(SRC, "shared/agents/help.md"))
    for path in agent_files:
        fm, body = strip_fm(path)
        body = apply_include(convert(body))
        servers = "Uses the krill-mcp-* and krill-mcp-%s-* MCP servers." % srv_suffix
        role = convert(fm.get("description", "")) + "\n\n" + body.strip()
        if not role.endswith("."):
            role += "."
        role += " " + servers
        slug = "help" if "/shared/agents/" in path else os.path.basename(path)[:-3]
        modes.append({
            "slug": "krill-" + slug,
            "name": "Krill " + slug.replace("-", " ").title(),
            "roleDefinition": role,
            "groups": ["read", "edit", "command", "mcp"],
        })
    with open(os.path.join(pdst, ".roomodes"), "w") as f:
        json.dump(modes, f, indent=2)
    with open(os.path.join(pdst, "mcp.json"), "w") as f:
        json.dump(mcp_servers(os.path.join(SRC, plugin, ".mcp.json")), f, indent=2)

    for skilldir in sorted(os.listdir(os.path.join(SRC, plugin, "skills"))):
        fm, body = strip_fm(os.path.join(SRC, plugin, "skills", skilldir, "SKILL.md"))
        out = workflow_md(skilldir, fm, apply_include(convert(body)))
        with open(os.path.join(pdst, "workflows", skilldir + ".md"), "w") as f:
            f.write(out)

# shared skills -> both plugins
for skilldir in ["status", "help"]:
    fm, body = strip_fm(os.path.join(SRC, "shared/skills", skilldir, "SKILL.md"))
    out = workflow_md(skilldir, fm, apply_include(convert(body)))
    for plugin in ["design", "work"]:
        with open(os.path.join(DST, plugin, "workflows", skilldir + ".md"), "w") as f:
            f.write(out)

with open(os.path.join(SRC, "shared/CONVENTIONS.md")) as f:
    conv = apply_include(convert(f.read()))
with open(os.path.join(DST, "shared/CONVENTIONS.md"), "w") as f:
    f.write(conv)

print("done")

