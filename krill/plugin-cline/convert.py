#!/usr/bin/env python3
"""Converter: krill Claude Code plugins -> Cline CLI plugins (agents + workflows)."""
import json, os, re

# Works from any checkout/worktree: lives at <root>/krill/plugin-cline/convert.py
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "plugin")
DST = os.path.join(ROOT, "plugin-cline")

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
                  lambda m: "Spawns one subagent (Cline: new_task) with the `krill-" + m.group(1) + "` agent as its mode", text)
    text = re.sub(r"[Dd]ispatch `(?:krill-design|krill-work):(" + P + r")`",
                  lambda m: ("Spawn" if m.group(0)[0] == "D" else "spawn") + " a subagent (Cline: new_task) with the `krill-" + m.group(1) + "` agent as its mode", text)
    text = re.sub(r"`(?:krill-design|krill-work):(" + P + r")`",
                  r"the `krill-\1` agent", text)
    # Skill slash commands -> workflow wording
    text = re.sub(r"`?/?krill-(?:design|work):(" + S + r")`?", r"the `\1` workflow", text)
    # MCP tool names -> plain server/tool wording
    text = re.sub(r"mcp__plugin_krill-(?:design|work)_krill-mcp-([a-z-]+)__([a-z_]+)\b",
                  r"the \2 tool on the krill-mcp-\1 MCP server", text)
    text = re.sub(r"mcp__plugin_krill-(?:design|work)_krill-mcp-([a-z-]+)__\*",
                  r"the tools of the krill-mcp-\1 MCP server", text)
    # Agent file paths -> agents (project-manager refs stay)
    text = re.sub(r"krill/plugin/(design|work)/agents/(\w+)\.md",
                  r"the krill-\2 agent in krill/plugin-cline/\1/agents/\2.md", text)
    text = re.sub(r"(?<!project-manager/)(?<![\w/-])agents/([\w-]+)\.md",
                  r"the krill-\1 agent in .cline/agents", text)
    # Skill paths -> workflows
    text = re.sub(r"skills/([a-z-]+)/SKILL\.md", r"workflows/\1.md", text)
    # Shared locations
    text = re.sub(r"krill/plugin/shared/skills/([a-z-]+)/",
                  r"krill/plugin-cline/{design,work}/workflows/\1.md", text)
    text = re.sub(r"shared/agents/help\.md", r"the krill-help agent in .cline/agents", text)
    text = re.sub(r"krill/plugin/shared/CONVENTIONS\.md",
                  r"krill/plugin-cline/shared/CONVENTIONS.md", text)
    text = re.sub(r"krill/plugin/", "krill/plugin-cline/", text)
    # Claude-isms
    text = re.sub(r"fresh `general-purpose` subagents?", "fresh subagents with the appropriate `krill-*` agent", text)
    text = text.replace("a fresh `general-purpose` subagent",
                        "a fresh subagent (Cline: new_task with the appropriate `krill-*` agent)")
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

def agent_md(slug, desc, prompt):
    # Cline CLI agent file: YAML frontmatter (name/description) + prompt body.
    def yaml_str(s):
        return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return ("---\n"
            f"name: {yaml_str('krill-' + slug)}\n"
            f"description: {yaml_str(desc)}\n"
            "---\n\n" + prompt.rstrip() + "\n")

os.makedirs(DST, exist_ok=True)
os.makedirs(os.path.join(DST, "shared"), exist_ok=True)
for plugin in ["design", "work"]:
    pdst = os.path.join(DST, plugin)
    os.makedirs(os.path.join(pdst, "workflows"), exist_ok=True)
    os.makedirs(os.path.join(pdst, "agents"), exist_ok=True)
    srv_suffix = plugin

    agent_files = [os.path.join(SRC, plugin, "agents", f)
                   for f in sorted(os.listdir(os.path.join(SRC, plugin, "agents")))]
    if "help.md" not in os.listdir(os.path.join(SRC, plugin, "agents")):
        agent_files.append(os.path.join(SRC, "shared/agents/help.md"))
    for path in agent_files:
        fm, body = strip_fm(path)
        prompt = apply_include(convert(body)).strip()
        servers = "Uses the krill-mcp-* and krill-mcp-%s-* MCP servers." % srv_suffix
        if not prompt.endswith("."):
            prompt += "."
        prompt += " " + servers
        desc_full = convert(fm.get("description", "")).strip()
        slug = "help" if "/shared/agents/" in path else os.path.basename(path)[:-3]
        # one-line description for the agent picker; full text stays in the body
        desc = desc_full.split("\n")[0]
        out = (desc_full + "\n\n" + prompt) if desc_full not in prompt else prompt
        with open(os.path.join(pdst, "agents", "krill-" + slug + ".md"), "w") as f:
            f.write(agent_md(slug, desc, out))
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

