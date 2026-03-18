# Review and Update Documentation

You are a documentation review orchestrator for the OTAPulse project. Your goal is to audit all documents in the `docs/` directory, identify outdated or incorrect information, and update them to reflect the current state of the codebase.

## Process

### Step 1: Discover documents

List all markdown files under `docs/` using Glob with pattern `docs/**/*.md`.

### Step 2: Understand the codebase

Before reviewing documents, explore the codebase to understand the current state:
- Read key source files referenced in docs (agent source, scripts, configs)
- Use Glob to find relevant implementation files
- Note current command names, file paths, API signatures, configuration keys, and feature flags

### Step 3: Spawn parallel review agents

Launch one Agent (subagent_type: general-purpose) per document in parallel. Each agent receives this prompt:

```
You are a documentation reviewer for the OTAPulse project — an embedded Linux OTA (Over-The-Air) update system.

Your task: Review and fix the document at <FILE_PATH>.

Steps:
1. Read the document fully using the Read tool.
2. Explore the codebase to verify every factual claim in the document:
   - Command names and flags (check scripts/, src/, CMakeLists.txt, Makefile, *.bb recipes)
   - File paths mentioned in examples
   - Configuration keys and their valid values
   - API endpoints and D-Bus interfaces
   - Version numbers, package names, dependencies
   - Build system instructions
3. Identify issues:
   - Incorrect command names or flags
   - Wrong file paths
   - Missing or removed features described as present
   - Features present in code but not documented
   - Outdated version numbers
   - Broken references to other docs
4. Fix the document using the Edit tool for each issue found.
5. Return a summary of all changes made (or "No changes needed" if the doc is accurate).

Working directory: c:/Users/krish/OneDrive/Documents/GitHub/OTA-Pulse
Document to review: <FILE_PATH>
```

Replace `<FILE_PATH>` with the actual document path for each agent.

### Step 4: Collect results and report

After all agents complete, compile a final report:
- List each document reviewed
- Summarize changes made per document
- Highlight any critical inaccuracies that were fixed
- List any issues that could not be auto-fixed and need manual attention

## Documents to review

Review all files found in Step 1, including:
- `docs/API.md`
- `docs/CONFIGURATION.md`
- `docs/INTEGRATION.md`
- `docs/SECURITY.md`
- `docs/TROUBLESHOOTING.md`
- `docs/TODO_BUILD_SYSTEMS.md`
- `docs/integration/README.md`
- `docs/integration/generic-arm-integration.md`
- `docs/integration/imx-integration.md`
- `docs/integration/raspberrypi-integration.md`
- `docs/integration/rockchip-integration.md`

## Important guidelines

- Do not change the document structure or style — only correct factual errors
- If a feature is described as "planned" or "TODO", verify if it has been implemented and update accordingly
- Preserve existing formatting, heading levels, and code block styles
- If you cannot determine whether something is correct without external knowledge (e.g., hardware specs), leave it unchanged and flag it in the report
- After all fixes, verify cross-references between documents are consistent
