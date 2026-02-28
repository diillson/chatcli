<p align="center">
  <a href="https://ai.edilsonfreitas.com/">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

# Bringing Your Terminal Closer to Artificial Intelligence 🕵️‍♂️✨

**ChatCLI** is an advanced command-line application (CLI) that integrates powerful Large Language Models (LLMs) (such as OpenAI, StackSpot, GoogleAI, ClaudeAI, xAI and Ollama -> `Local models`) to facilitate interactive and contextual conversations directly in your terminal. Designed for developers, data scientists, and tech enthusiasts, it enhances productivity by aggregating various contextual data sources and offering a rich, user-friendly experience.

<p align="center">
  <em>See ChatCLI in action, including Agent Mode and provider switching.</em><br>
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI Demo" width="800">
</p>

<div align="center">
  <img src="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg"/>
  <a href="https://github.com/diillson/chatcli/releases">
    <img src="https://img.shields.io/github/v/release/diillson/chatcli"/>
  </a>
    <img src="https://img.shields.io/github/last-commit/diillson/chatcli"/>  
    <img src="https://img.shields.io/github/languages/code-size/diillson/chatcli"/>  
    <img src="https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go%20Version"/>  
    <img src="https://img.shields.io/github/license/diillson/chatcli"/>
</div>

---

> 📘 Explore the detailed documentation — including use cases, tutorials, and recipes — at [diillson.github.io/chatcli](https://diillson.github.io/chatcli)

-----

### 📝 Table of Contents

- [Why Use ChatCLI?](#why-use-chatcli)
- [Key Features](#key-features)
- [Multi-language Support (i18n)](#multi-language-support-i18n)
- [Installation](#installation)
- [Configuration](#configuration)
- [Authentication (OAuth)](#authentication-oauth)
- [Usage and Commands](#usage-and-commands)
    - [Interactive Mode](#interactive-mode)
    - [Non-Interactive Mode (One-Shot)](#non-interactive-mode-one-shot)
    - [CLI Commands](#cli-commands)
- [Advanced File Processing](#advanced-file-processing)
    - [Modes of `@file` Usage](#modes-of-file-usage)
    - [Chunking System in Detail](#chunking-system-in-detail)
    - [Persistent Context Management](#persistent-context-management)
- [Agent Mode](#agent-mode)
    - [Security Policy](#security-policy)
    - [Agent Interaction](#agent-interaction)
    - [Enhanced Agent UI](#enhanced-agent-ui)
    - [Agent One-Shot Mode](#agent-one-shot-mode)
- [Customizable Agents (Personas)](#customizable-agents-personas)
    - [Concept](#concept)
    - [File Structure](#file-structure)
    - [Management Commands](#management-commands)
    - [Practical Example](#practical-example)
- [Remote Server Mode (gRPC)](#remote-server-mode-grpc)
- [Kubernetes Monitoring (K8s Watcher)](#kubernetes-monitoring-k8s-watcher)
- [Code Structure and Technologies](#code-structure-and-technologies)
- [Contributing](#contributing)
- [License](#license)
- [Contact](#contact)

-----

## Why Use ChatCLI?

- **Unified Interface**: Access the best models on the market (OpenAI, Claude, Gemini, etc.) and local models (Ollama) from a single interface, without needing to switch tools.
- **Context-Aware**: Commands like `@git`, `@file`, and `@history` inject relevant context directly into your prompt, allowing the AI to understand your work environment and provide more accurate answers.
- **Automation Powerhouse**: **Agent Mode** transforms the AI into a proactive assistant that can execute commands, create files, and interact with your system to solve complex tasks.
- **Developer-Centric**: Built for the development workflow, with features like smart code file processing, command execution, and Git integration.

-----

## Key Features

- **Support for Multiple Providers**: Switch between OpenAI, StackSpot, ClaudeAI, GoogleAI, xAI and Ollama -> `Local models`.
- **Interactive CLI Experience**: Command history navigation, auto-completion, and visual feedback (`"Thinking..."`).
- **Powerful Contextual Commands**:
    - `@history` – Inserts the last 10 shell commands (supports bash, zsh, and fish).
    - `@git` – Adds information about the current Git repository (status, commits, and branches).
    - `@env` – Includes environment variables in the context.
    - `@file <path>` – Inserts file or directory content with support for `~` expansion and relative paths.
    - `@command <command>` – Executes a system command and adds its output to the context.
    - `@command -i <command>` – Executes interactive system commands and **DOES NOT** add the output to the context.
    - `@command --ai <command> > <context>` – Executes a command and sends its output directly to the LLM with additional context.
- **Recursive Directory Exploration**: Processes entire projects while ignoring irrelevant folders (e.g., `node_modules`, `.git`).
- **Dynamic Configuration and Persistent History**: Change providers, update configurations in real-time, and maintain history across sessions.
- **Robustness**: Exponential backoff retry for handling external API errors.
- **Smart Paste Detection**: Automatically detects pasted text in the terminal via *Bracketed Paste Mode*. Large pastes (> 150 chars) are replaced by a compact placeholder (`«N chars | M lines»`) to prevent terminal corruption, with the real content preserved and sent on Enter.
- **Advanced Prompt Navigation**: Keyboard shortcuts with Alt/Ctrl/Cmd + arrow keys for word and line navigation, compatible with major macOS terminals (Terminal.app, iTerm2, Alacritty, Kitty, WezTerm).
- **Parallel Mode Security**: Multi-agent workers fully respect `coder_policy.json`, with serialized, contextual security prompts showing which agent is requesting each action.
- **Remote Resource Discovery**: When connecting to a server, the client automatically discovers available plugins, agents, and skills on the server. Remote plugins can be executed on the server or downloaded locally; remote agents and skills are transferred and composed locally, merging with local resources.
- **Hardened Security**: Constant-time token comparison, shell injection prevention, editor validation, gRPC reflection disabled by default, and hardened containers (read-only, no-new-privileges, drop ALL capabilities). See the [security documentation](https://diillson.github.io/chatcli/docs/features/security/).

-----

## Multi-language Support (i18n)

ChatCLI is designed to be global. The user interface, including menus, tips, and status messages, is fully internationalized.

- **Automatic Detection**: The language is automatically detected from your system's environment variables (`CHATCLI_LANG`(major priority), `LANG` or `LC_ALL`).
- **Supported Languages**: Currently, ChatCLI supports **English (en)** and **Portuguese (pt-BR)**.
- **Fallback**: If your system's language is not supported, the interface will default to English.

-----

## Installation

### Prerequisites

- **Go (version 1.23+)**: [Available at golang.org](https://golang.org/dl/).

### 1. From GitHub Releases (Recommended)

The easiest way to install is to download the appropriate binary for your operating system and architecture from the [GitHub Releases page](https://github.com/diillson/chatcli/releases).

### 2. Installation via `go install`

```bash
go install github.com/diillson/chatcli@latest
```
This will install the binary to your  $GOPATH/bin  folder, allowing you to run  chatcli  directly from your terminal if  $GOPATH/bin  is in your  PATH .

### 3. Build from Source

1. Clone the Repository:
```bash
   git clone https://github.com/diillson/chatcli.git
   cd chatcli
```
2. Install Dependencies and Compile:
```bash
   go mod tidy
   go build -o chatcli
```

3. To compile with version information:
```bash
   VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
   COMMIT_HASH=$(git rev-parse --short HEAD)
   BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    go build -ldflags "\
      -X github.com/diillson/chatcli/version.Version=${VERSION} \
      -X github.com/diillson/chatcli/version.CommitHash=${COMMIT_HASH} \
      -X github.com/diillson/chatcli/version.BuildDate=${BUILD_DATE}" \
      -o chatcli main.go
```
This injects version data into the binary, accessible via  /version  or  chatcli --version .

--------

## Configuration

ChatCLI uses environment variables to define its behavior and connect to LLM providers. The easiest way is to create a  .env  file in the project's root directory.

### Essential Environment Variables

- General:
  -  `CHATCLI_DOTENV`  – **(Optional)** Defines the path to your  `.env`  file.
  -  `CHATCLI_IGNORE` – **(Optional)** Defines a list of files or folders to be ignored by ChatCLI.
  -  `CHATCLI_LANG`  – **(Optional)** Sets the interface language ( e.g.,  en ,  `pt-BR` ). Default: detects from system.
  -  `LOG_LEVEL`  ( `debug` ,  `info` ,  `warn` ,  `error` )
  -  `LLM_PROVIDER`  ( `OPENAI` ,  `STACKSPOT` ,  `CLAUDEAI` ,  `GOOGLEAI` ,  `XAI` )
  -  `MAX_RETRIES`  - **(Optional)** Maximum number of attempts for API calls (default:  `5` ).
  -  `INITIAL_BACKOFF`  - **(Optional)** Initial wait time between attempts (default:  `3`  - seconds`).
  - `LOG_FILE` - **(Optional)** Path to the log file (default: `$HOME/.chatcli/app.log`).
  - `LOG_MAX_SIZE` - **(Optional)** Maximum size of the log file before rotation (default: 100MB).
  - `HISTORY_MAX_SIZE` - **(Optional)** Maximum size of the history file before rotation (default: 100MB).
  - `HISTORY_FILE` - **(Optional)** Path to the history file (supports `~`). Default: `.chatcli_history`.
  - `ENV`  - **(Optional)** Defines how the log will be displayed ( `dev `,  `prod `). Default:  `dev` .
      -  dev  displays the logs directly in the terminal and saves them to the log file.
      -  prod  only saves them to the log file, keeping the terminal cleaner.

- Providers:
  -  OPENAI_API_KEY ,  OPENAI_MODEL ,  OPENAI_ASSISTANT_MODEL ,  OPENAI_MAX_TOKENS ,  OPENAI_USE_RESPONSES
  -  ANTHROPIC_API_KEY ,  ANTHROPIC_MODEL ,  ANTHROPIC_MAX_TOKENS ,  ANTHROPIC_API_VERSION
  -  GOOGLEAI_API_KEY ,  GOOGLEAI_MODEL ,  GOOGLEAI_MAX_TOKENS
  -  XAI_API_KEY ,  XAI_MODEL ,  XAI_MAX_TOKENS
  -  OLLAMA_ENABLED ,  OLLAMA_BASE_URL ,  OLLAMA_MODEL ,  OLLAMA_MAX_TOKENS ,  OLLAMA_FILTER_THINKING  – (Optional) Filters "thinking aloud" from models like Qwen3 (true/false, default: true)
  -  CLIENT_ID ,  CLIENT_KEY ,  STACKSPOT_REALM ,  STACKSPOT_AGENT_ID  (for StackSpot)
- Agent:
  -  `CHATCLI_AGENT_CMD_TIMEOUT`  – **(Optional)** Default timeout for each command executed from the action list by Agent Mode. Accepts Go durations (e.g., 30s, 2m, 10m). Default:  10m . Maximum: 1h.
  -  `CHATCLI_AGENT_DENYLIST`  – **(Optional)** Semicolon-separated list of regular expressions to block extra dangerous commands. Example: rm\s+-rf\s+.;curl\s+[^|;]|\s*(sh|bash).
  -  `CHATCLI_AGENT_ALLOW_SUDO`  – **(Optional)** Allow sudo commands without automatic blocking (true/false). Default:  false  (sudo is blocked for safety).
  -  `CHATCLI_AGENT_PLUGIN_MAX_TURNS` - **(Optional)** Defines the maximum number of turns the agent can have. Default: 50. Maximum: 200.
  -  `CHATCLI_AGENT_PLUGIN_TIMEOUT` - **(Optional)** Defines the execution timeout for the agent plugin (e.g., 30s, 2m, 10m). Default: 15 (Minutes)
- Multi-Agent (Parallel Orchestration):
  -  `CHATCLI_AGENT_PARALLEL_MODE`  – **(Optional)** Controls multi-agent mode with parallel orchestration. **Enabled by default.** Set to `false` to disable. Default: `true`.
  -  `CHATCLI_AGENT_MAX_WORKERS`  – **(Optional)** Maximum number of workers (goroutines) running agents simultaneously. Default: `4`.
  -  `CHATCLI_AGENT_WORKER_MAX_TURNS`  – **(Optional)** Maximum turns for each worker agent's mini ReAct loop. Default: `10`.
  -  `CHATCLI_AGENT_WORKER_TIMEOUT`  – **(Optional)** Timeout per individual worker agent. Accepts Go durations (e.g., 30s, 2m, 10m). Default: `5m`.
- OAuth:
  -  `CHATCLI_OPENAI_CLIENT_ID`  – **(Optional)** Override the OpenAI OAuth client ID.


### Example  .env

    # General Settings
    LOG_LEVEL=info
    CHATCLI_LANG=en_US
    CHATCLI_IGNORE=~/.chatignore
    ENV=prod
    LLM_PROVIDER=CLAUDEAI
    MAX_RETRIES=10
    INITIAL_BACKOFF=2
    LOG_FILE=app.log
    LOG_MAX_SIZE=300MB
    HISTORY_MAX_SIZE=300MB
    HISTORY_FILE=~/.chatcli_history

    # Agent Settings
    CHATCLI_AGENT_CMD_TIMEOUT=2m   # The command will have 2m to run after that it is locked and finished (max: 1h)
    CHATCLI_AGENT_DENYLIST=rm\\s+-rf\\s+.*;curl\\s+[^|;]*\\|\\s*(sh|bash);dd\\s+if=;mkfs\\w*\\s+
    CHATCLI_AGENT_ALLOW_SUDO=false
    CHATCLI_AGENT_PLUGIN_MAX_TURNS=50
    CHATCLI_AGENT_PLUGIN_TIMEOUT=20m

    # Multi-Agent (Parallel Orchestration)
    CHATCLI_AGENT_PARALLEL_MODE=true        # Disable with false if needed
    CHATCLI_AGENT_MAX_WORKERS=4             # Maximum agents running in parallel
    CHATCLI_AGENT_WORKER_MAX_TURNS=10       # Maximum turns per worker agent
    CHATCLI_AGENT_WORKER_TIMEOUT=5m         # Timeout per worker agent

    # OAuth Settings (optional)
    # CHATCLI_OPENAI_CLIENT_ID=custom-client-id    # Override the OpenAI OAuth client ID
    
    # OpenAI Settings
    OPENAI_API_KEY=your-openai-key
    OPENAI_MODEL=gpt-4o-mini
    OPENAI_ASSISTANT_MODEL=gpt-4o-mini
    OPENAI_USE_RESPONSES=true    # use the Responses API (e.g., for gpt-5)
    OPENAI_MAX_TOKENS=60000
    
    # StackSpot Settings
    CLIENT_ID=your-client-id
    CLIENT_KEY=your-client-key
    STACKSPOT_REALM=your-realm
    STACKSPOT_AGENT_ID=your-agent-id
    
    # ClaudeAI Settings
    ANTHROPIC_API_KEY=your-claudeai-key
    ANTHROPIC_MODEL=claude-sonnet-4-5
    ANTHROPIC_MAX_TOKENS=20000
    ANTHROPIC_API_VERSION=2023-06-01
    
    # Google AI (Gemini) Settings
    GOOGLEAI_API_KEY=your-googleai-key
    GOOGLEAI_MODEL=gemini-2.5-flash
    GOOGLEAI_MAX_TOKENS=20000
    
    # xAI Settings
    XAI_API_KEY=your-xai-key
    XAI_MODEL=grok-4-latest
    
    # Ollama Settings
    OLLAMA_ENABLED=true      #Required for enabled API Ollama
    OLLAMA_BASE_URL=http://localhost:11434
    OLLAMA_MODEL=gpt-oss:20b
    OLLAMA_MAX_TOKENS=5000
    OLLAMA_FILTER_THINKING=true  # Filters intermediate reasoning in responses (e.g. for Qwen3, llama3... - THIS IS REQUIRED TO BE TRUE for Agent mode. Works well with some OLLAMA models that have "out loud" reasoning)

    # Remote Server Settings (chatcli serve)
    CHATCLI_SERVER_PORT=50051
    CHATCLI_SERVER_TOKEN=my-secret-token
    # CHATCLI_SERVER_TLS_CERT=/path/to/cert.pem
    # CHATCLI_SERVER_TLS_KEY=/path/to/key.pem

    # Remote Client Settings (chatcli connect)
    # CHATCLI_REMOTE_ADDR=myserver:50051
    # CHATCLI_REMOTE_TOKEN=my-secret-token
    # CHATCLI_CLIENT_API_KEY=sk-xxx    # Your own API key (forwarded to server)

    # K8s Watcher Settings (chatcli watch / chatcli serve --watch-*)
    # CHATCLI_WATCH_DEPLOYMENT=myapp          # Single deployment (legacy)
    # CHATCLI_WATCH_NAMESPACE=production
    # CHATCLI_WATCH_INTERVAL=30s
    # CHATCLI_WATCH_WINDOW=2h
    # CHATCLI_WATCH_MAX_LOG_LINES=100
    # CHATCLI_WATCH_CONFIG=/path/targets.yaml  # Multi-target (via config YAML)
    # CHATCLI_KUBECONFIG=~/.kube/config

--------

## Authentication (OAuth)

ChatCLI supports **two authentication methods** for providers that offer OAuth:

1. **API Key (traditional)**: Set the environment variable (e.g., `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) in your `.env` file.
2. **OAuth (interactive login)**: Authenticate directly from the terminal using `/auth login`, no need to manually generate or paste keys.

> OAuth is ideal for users on **ChatGPT Plus / Codex** (OpenAI) or **Claude Pro** (Anthropic) plans who don't want to manage API keys.

### `/auth` Commands

| Command | Description |
|---------|-------------|
| `/auth status` | Shows authentication status for all providers |
| `/auth login openai-codex` | Starts the OAuth flow with OpenAI (opens browser automatically) |
| `/auth login anthropic` | Starts the OAuth flow with Anthropic |
| `/auth logout openai-codex` | Removes OpenAI OAuth credentials |
| `/auth logout anthropic` | Removes Anthropic OAuth credentials |

### How it Works

1. Run `/auth login openai-codex` (or `anthropic`)
2. Your browser opens automatically to the provider's login page
3. **OpenAI:** the token is captured automatically via local callback (port 1455)
4. **Anthropic:** after authorizing, copy the code shown on the page and paste it in the terminal
5. The provider appears immediately in `/switch` — no restart needed
6. Credentials are stored with **AES-256-GCM encryption** at `~/.chatcli/auth-profiles.json`

### Which Endpoint is Used (OpenAI)

| Authentication Method | Endpoint Used |
|-----------------------|---------------|
| `OPENAI_API_KEY` (manual key) | `api.openai.com/v1/responses` or `/v1/chat/completions` |
| `/auth login openai-codex` (OAuth) | `chatgpt.com/backend-api/codex/responses` |

> ChatCLI automatically detects the credential type and routes to the correct endpoint.

### Starting Without Credentials

ChatCLI can be started **without any API keys or OAuth login** configured. In this case, the app opens normally and you can use `/auth login` to authenticate. After login, use `/switch` to select the provider.

--------

## Usage and Commands

│ Pro-Tip: Create a shell alias for quick access! Add  alias c='chatcli'  to your  .bashrc ,  .zshrc , or  config.fish .

### Interactive Mode

Start the application with  ./chatcli  and begin your conversation.

### Non-Interactive Mode (One-Shot)

Execute prompts in a single line, ideal for scripting and automation.

- Quick examples:
  - chatcli -p "Quickly explain this repository."
  - chatcli -p "@git @env Create a concise release note."
  - chatcli -p "@file ./src --mode summary Provide an overview of the architecture."
  - chatcli -p "@file ./myproject Describe the architecture of this project based on the .go files" \
    --provider STACKSPOT \
    --agent-id "your-agent-id-here"

- Input via  stdin  (Pipes):
  - git diff | chatcli -p "Summarize the changes and list potential impacts."
  - cat error.log | chatcli -p "Explain the root cause of this error and suggest a solution."

- Available One-Shot Flags:
  -  -p  or  --prompt : The text to send to the LLM for a single execution.
  -  --provider : Overrides the LLM provider at runtime ( OPENAI ,  OPENAI_ASSISTANT ,  CLAUDEAI ,  GOOGLEAI ,  STACKSPOT ,  XAI ).
  -  --model : Chooses the model for the active provider (e.g.,  gpt-4o-mini ,  claude-sonnet-4-5 ,  gemini-2.5-flash , etc.).
  -  --max-tokens : Defines the maximum amount of tokens used for active provider.
  -  --realm : Overrides the StackSpot realm at runtime.
  -  --agent-id : Overrides the StackSpot agent ID at runtime.
  -  --timeout : Sets the timeout for the one-shot call (default:  5m ).
  -  --no-anim : Disables animations (useful in scripts/CI).
  -  --agent-auto-exec : Automatically executes the first command suggested by the agent (in agent mode).


Note: The same contextual features work within the  --prompt  text, such as  @file ,  @git ,  @env ,  @command , and the  >  operator to add context. Remember to enclose the prompt in double quotes in the shell to avoid unwanted interpretations.

### CLI Commands

- Session Management:
    -  /session save <name> ,  /session load <name> ,  /session list ,  /session delete <name> ,  /session new
- Configuration and Status:
  -  /switch ,  /reload ,  /config  or  /status  (displays runtime settings, current provider, and model).
- Authentication:
  -  `/auth status` ,  `/auth login <provider>` ,  `/auth logout <provider>`
- General:
  -  /help : Displays help information.
  -  /exit : To exit ChatCLI.
  -  /version  or  /v : Shows the version, commit hash, and build date.
  -  Ctrl+C  (once): Cancels the current operation.
  -  Ctrl+C  (twice) or  Ctrl+D : Exits the application.
- Context:
  -  @history ,  @git ,  @env ,  @file ,  @command .

--------

## Advanced File Processing

The  `@file` <path>  command is the primary tool for sending files and directories, with support for path expansion ( ~ ).

### Modes of  @file  Usage

- Default Mode ( full ): Processes the entire content of a file or directory, truncating it if the token limit is exceeded. Ideal for small to medium-sized projects.
- Summary Mode ( summary ): Returns only the directory structure, file list with sizes, and general statistics. Useful for getting an overview without the content.
- Smart Mode ( smart ): ChatCLI assigns a relevance score to each file based on your question and includes only the most pertinent ones.
@file --mode smart ~/my-project/ How does the login system work?

- Chunked Mode ( chunked ): For large projects, it splits the content into manageable chunks, sending one at a time.

### Chunking System in Detail

After the first chunk is sent, use  /nextchunk  to process the next. The system provides visual feedback on progress and the number of remaining chunks. To manage failures, use  /retry ,  /retryall , or  /skipchunk .

Claro! Aqui está o conteúdo **formatado corretamente em Markdown**, perfeito para colocar no seu `README.md` 👇


## Persistent Context Management

**ChatCLI** provides a powerful system for creating, saving, and reusing complex contexts across sessions with the `/context` command.

### 💡 Why use persistent contexts?

- **Reusability:** Define a project's scope once and attach it to any future conversation with a single command.  
- **Consistency:** Ensure the AI always has the same baseline knowledge about your project.  
- **Efficiency:** Avoid typing long `@file` commands repeatedly.  
- **Working with Large Projects:** Create contexts in `chunked` mode and attach only the relevant parts (`--chunk N`) for your current task — saving tokens and focusing the AI's analysis.

### ⚙️ Main `/context` Commands

#### 🆕 Create a new context

```bash
/context create <name> <paths...> [options]

# Example: Create a "smart" context with tags
/context create my-api ./src ./docs --mode smart --tags "golang,api"
````

**Available options:**

* `--mode` or `-m`: Defines the processing mode

    * `full`: Complete file contents
    * `summary`: Directory structure and metadata only
    * `chunked`: Splits into manageable chunks
    * `smart`: AI selects relevant files based on your prompt
* `--description` or `-d`: Adds a text description to the context
* `--tags` or `-t`: Adds tags for organization (comma-separated)

#### 📋 List all contexts

```bash
/context list
```

**Example output:**

```
🧩 my-project        Backend REST API — mode:chunked | 4 chunks | 2.3 MB | tags:api,golang
📄 docs              Documentation — mode:full | 12 files | 156 KB | tags:docs
🧩 frontend          React Interface — mode:chunked | 3 chunks | 1.8 MB | tags:react,ui
```

#### 🔍 Show context details

```bash
/context show <name>
```

Displays complete and structured information about the context:

##### 📊 General Information

* Name, ID, and description
* Processing mode (`full`, `summary`, `chunked`, `smart`)
* Number of files and total size
* Associated tags
* Creation and last update dates

##### 📂 Distribution by Type

* Statistics of file types present
* Percentage and size occupied by each type

**Example:**

```
● Go:            98 files (62.8%) | 1847.32 KB
● JSON:          12 files (7.7%)  | 45.67 KB
● Markdown:       8 files (5.1%)  | 123.45 KB
```

##### 🧩 Chunk Structure (for chunked contexts)

* Lists all chunks with their respective information
* Description of each chunk
* Files contained in each chunk organized in a tree
* Size and token estimate per chunk

##### 📁 File Structure (for full/summary contexts)

* Directory and file tree
* Type and size of each file
* Organized hierarchical visualization

##### 📌 Attachment Status

* Tips on how to attach the context
* Available commands for specific chunks

#### 🧠 Inspect a context (deep analysis)

```bash
/context inspect <name> [--chunk N]
```

The `inspect` command provides detailed statistical analysis of the context:

##### 📊 Statistical Analysis

* Total lines of code
* Average lines per file
* Size distribution (small, medium, large)

##### 🗂️ Extensions Found

* List of all file extensions
* Number of files per extension

##### 🧩 Chunk Analysis (if applicable)

* Average, minimum, and maximum chunk sizes
* Percentage variation between chunks
* Content distribution

**Inspect a specific chunk:**

```bash
/context inspect my-project --chunk 1
```

Displays:

* Chunk description
* Complete file list
* Lines of code per file
* Individual size of each file

#### 📎 Attach context to current session

```bash
/context attach <name> [options]
```

**Available options:**

* `--priority` or `-p <number>`: Sets priority (lower = sent first)
* `--chunk` or `-c <number>`: Attaches only a specific chunk
* `--chunks` or `-C <numbers>`: Attaches multiple chunks (e.g., `1,2,3`)

**Examples:**

```bash
# Attach complete context
/context attach my-api

# Attach only chunk 1
/context attach my-project --chunk 1

# Attach chunks 1, 2, and 3
/context attach my-project --chunks 1,2,3

# Attach with high priority (will be sent first)
/context attach docs --priority 1
```

#### 🔌 Detach context

```bash
/context detach <name>
```

#### 📚 View attached contexts

```bash
/context attached
```

Shows all contexts currently attached to the session
with their priorities and selected chunks.

#### 🗑️ Delete a context

```bash
/context delete <name>
```

> Asks for confirmation before permanently deleting.

### 🎯 Additional Commands

#### 🔀 Merge contexts

```bash
/context merge <new-name> <context1> <context2> [...]
```

**Example:**

```bash
/context merge complete-project backend frontend infra
```

#### 📤 Export context

```bash
/context export <name> <file-path.json>
```

**Example:**

```bash
/context export my-api ./backups/api-context.json
```

#### 📥 Import context

```bash
/context import <file-path.json>
```

**Example:**

```bash
/context import ./backups/api-context.json
```

#### 📈 Usage metrics

```bash
/context metrics
```

Displays statistics about:

* Most used contexts
* Total size occupied
* Usage frequency

#### 🆘 Complete help

```bash
/context help
```

---

### Advanced File Filtering with `.chatignore`

To further refine the context sent to the AI, `ChatCLI` supports a file and directory exclusion system inspired by `.gitignore`. This allows you to avoid sending test files, documentation, logs, or any other irrelevant content.

#### Why Filter Files?

*   🎯 **Focus**: Sends only relevant source code to the AI, resulting in more accurate answers.
*   💰 **Efficiency**: Saves tokens, which can reduce costs on paid APIs.
*   🚀 **Speed**: Processes large projects faster by ignoring unnecessary files.
*   🔇 **Noise Reduction**: Avoids polluting the context with compiled files, dependencies, or logs.

#### How it Works: The `.chatignore` File

The syntax is identical to `.gitignore`:

*   Lines starting with `#` are comments.
*   To ignore a directory and all its contents, add the directory name followed by a `/` (e.g., `docs/`).
*   Use glob patterns (wildcards) to ignore files (e.g., `*_test.go`, `*.log`).

#### Rule Precedence Hierarchy

`ChatCLI` looks for an ignore file in a specific order. The first one found is used, and the others are ignored.

1.  **Environment Variable (Highest Priority)**: If the `CHATCLI_IGNORE` environment variable is set to a file path, **only** that file will be used.
    ```bash
    export CHATCLI_IGNORE_PATH="~/configs/my_global_ignore.txt"
    ```

2.  **Project-Specific File**: If the environment variable is not set, `ChatCLI` will look for a `.chatignore` file in the **root of the directory** you are analyzing with `@file`. This is ideal for project-specific rules.

3.  **Global User File**: If none of the above are found, it will look for a global ignore file at `~/.chatcli/.chatignore`. This is perfect for rules that apply to all your projects (e.g., `.DS_Store`).

4.  **Built-in Defaults**: If no ignore file is found, `ChatCLI` will use its internal default rules (which already ignore `.git`, `node_modules`, etc.).

> **Important Note:** The rules are not merged. Only the first ignore file found in the hierarchy is used.

#### Practical Example of a `.chatignore` File

You can create this file in your project's root to ignore test files, documentation, and CI configurations.


***.chatignore:***
```
Ignore all Go test files

*_test.go

Ignore entire directories for documentation and end-to-end tests

docs/
e2e/

Ignore specific CI and log files

golangci.yml
*.log
```

--------

## Agent Mode

Agent Mode allows the AI to interact with your system, suggesting or executing commands to automate complex or repetitive tasks.

-----

### Coder Mode Security and Governance

Coder Mode (`/coder`) features a robust governance system inspired by ClaudeCode, ensuring you have total control over AI actions.

1. **Allow**: Read actions (`ls`, `read`) are executed automatically.
2. **Deny**: Dangerous actions can be permanently blocked.
3. **Ask**: By default, writes and executions require interactive approval.

> 🛵 Learn more about configuring security rules in the [complete documentation](https://diillson.github.io/chatcli/docs/features/coder-security).

#### Coder Mode Tools (@coder)

`@coder` is a **builtin plugin** — it ships embedded in the ChatCLI binary and works out-of-the-box, no separate installation required.

The `@coder` contract supports **JSON args** (recommended) while keeping single-line CLI compatibility. Examples:

- JSON (recommended): `<tool_call name="@coder" args="{\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}"/>`
- CLI (legacy): `<tool_call name="@coder" args="read --file main.go"/>`

Key new subcommands:

- `git-status`, `git-diff`, `git-log`, `git-changed`, `git-branch`
- `test` (auto stack detection)
- `patch --diff` (unified diff, text/base64)

Full details in the guide: https://diillson.github.io/chatcli/docs/features/coder-plugin/

#### Security Policy

ChatCLI prioritizes safety by blocking dangerous commands by default. You can strengthen this policy with environment variables:

-  `CHATCLI_AGENT_DENYLIST`  to block additional patterns (regex, separated by semicolons `;`).
-  `CHATCLI_AGENT_ALLOW_SUDO`  to allow/deny  sudo  without automatic blocking (default:  `false`).
-  `CHATCLI_GRPC_REFLECTION`  to enable gRPC reflection on the server (default: `false` — disabled in production).
-  `CHATCLI_DISABLE_VERSION_CHECK`  to disable automatic version checking (`true`/`false`).

Even when allowed, dangerous commands may still require explicit confirmation in the terminal.

> For complete details on all ChatCLI security measures, see the [security documentation](https://diillson.github.io/chatcli/docs/features/security/).

#### Coder Mode Policy Files (Local vs Global)

By default, policies are stored in `~/.chatcli/coder_policy.json`. You can also add a **project-local** policy file:

- Local file: `./coder_policy.json` (project root)
- Global file: `~/.chatcli/coder_policy.json`

Local policy behavior:

- If `merge` is **true**, local rules merge with global (local overrides same pattern).
- If `merge` is **false** or omitted, **only** the local rules are used.

Example (local with merge):
```json
{
  "merge": true,
  "rules": [
    { "pattern": "@coder write", "action": "ask" },
    { "pattern": "@coder exec --cmd 'rm -rf'", "action": "deny" }
  ]
}
```

#### Coder Mode UI Settings

You can control the `/coder` UI style and the tips banner with env vars:

- `CHATCLI_CODER_UI`:
  - `full` (default)
  - `minimal`
- `CHATCLI_CODER_BANNER`:
  - `true` (default, shows the quick cheat sheet)
  - `false`

These values are visible in `/status` and `/config`.

#### Multi-Agent Orchestration

ChatCLI includes a multi-agent orchestration system **enabled by default** in `/coder` and `/agent` modes. The orchestrator LLM automatically decides when to dispatch specialized agents in parallel for complex tasks.

**12 Built-in Specialized Agents:**

| Agent | Expertise | Access |
|-------|-----------|--------|
| **FileAgent** | Code reading and analysis | Read-only |
| **CoderAgent** | Code writing and modification | Read/Write |
| **ShellAgent** | Command execution and testing | Execution |
| **GitAgent** | Version control | Git ops |
| **SearchAgent** | Codebase search | Read-only |
| **PlannerAgent** | Reasoning and task decomposition | No tools (pure LLM) |
| **ReviewerAgent** | Code review and quality analysis | Read-only |
| **TesterAgent** | Test generation and coverage analysis | Read/Write/Execution |
| **RefactorAgent** | Safe structural code transformations | Read/Write |
| **DiagnosticsAgent** | Troubleshooting and root cause analysis | Read/Execution |
| **FormatterAgent** | Code formatting and style normalization | Write/Execution |
| **DepsAgent** | Dependency management and auditing | Read/Execution |

Each agent has its own **skills** — some are accelerator scripts (execute without LLM calls), others are descriptive (the agent resolves them via its mini ReAct loop).

**Custom Agents as Workers:** Persona agents defined in `~/.chatcli/agents/` are automatically loaded as workers in the orchestration system. The LLM can dispatch them via `<agent_call agent="devops" task="..." />` with the same ReAct loop, parallel reads, and error recovery as built-in agents. The `tools` field in the YAML frontmatter defines which commands the agent can use (Read→read, Grep→search, Bash→exec/test/git-*, Write→write, Edit→patch).

**Error Recovery Strategy:** When an agent fails, the orchestrator switches to direct `tool_call` to diagnose and fix (it already has the error context). After the fix, it resumes `agent_call` for the next work phase.

> Disable with `CHATCLI_AGENT_PARALLEL_MODE=false` if needed. Full documentation at [diillson.github.io/chatcli/docs/features/multi-agent-orchestration](https://diillson.github.io/chatcli/docs/features/multi-agent-orchestration/)

### Agent Interaction

Start the agent with  /agent <query>  or  /run <query> . The agent will suggest commands that you can approve or refine.

- Refining: Use  pCN  to add context before executing command  N .
- Adding context to the output: After execution, use  aCN  to add information to the output of command  N  and get a new response from the AI.

### Enhanced Agent UI

- Compact vs. Full Plan: Toggle with the  p  key for a summary or detailed view of the execution plan.
- Anchored Last Result: The output of the last executed command stays fixed at the bottom, making it easy to reference without scrolling.
- Quick Actions:
  -  vN : Opens the full output of command  N  in your pager ( less  or  more ), ideal for long logs.
  -  wN : Saves the output of command  N  to a temporary file for later analysis or sharing.
  -  r : Redraws the screen, useful for clearing the view.

## 🔌 Plugin System

ChatCLI supports a plugin system to extend its functionality and automate complex tasks. A plugin is a simple executable that follows a specific contract, allowing ChatCLI to discover, execute, and interact with it securely.

This allows you to create custom commands (such as @kind) that can orchestrate tools, interact with APIs, or perform any logic you can program.

### For Users: Managing Plugins

You can manage installed plugins using the /plugin command.

#### List Installed Plugins

To see all available plugin commands:

/plugin list

#### Install a New Plugin

You can install a plugin directly from a Git repository. ChatCLI will clone, compile (if using Go), and install the executable in the correct directory.

/plugin install https://github.com/username/my-plugin-chatcli.git

> ⚠️ Security Warning: Installing a plugin involves downloading and running third-party code on your machine. Only install plugins from sources you fully trust.

#### View Plugin Details

To see the description and how to use a specific plugin:

/plugin show <plugin-name>

#### Uninstall a Plugin

To remove a plugin:

/plugin uninstall <plugin-name>

#### Reload Plugins

ChatCLI monitors the plugin directory and automatically reloads if there are changes. If you need to force a manual reload:

/plugin reload

--------

### For Developers: Creating Your Own Plugin

Creating a plugin is simple. Just create an executable program that follows the ChatCLI "contract".

#### The Plugin Contract

1. Executable: The plugin must be an executable file.

2. Location: The executable file must be placed in the ~/.chatcli/plugins/ directory.

3. Command Name: The command name will be @ followed by the name of the executable file. Ex: a file called kind will be invoked as @kind.

4. Metadata (--metadata): The executable must respond to the --metadata flag. When called with this flag, it should print a JSON containing the following information to standard output (stdout):

```
{
"name": "@my-command",
"description": "A brief description of what the plugin does.",

"usage": "@my-command <subcommand> [--flag value]",
"version": "1.0.0"

}
```
5. Communication and Feedback (stdout vs stderr): This is the most important part for a good user experience.

- Standard Output (stdout): Use standard output only for the final result that should be returned to chatcli and potentially sent to the AI.

- Error Output (stderr): Use error output for all progress logs, status, warnings, and messages to the user. chatcli will display stderr in real time, avoiding the feeling that the program has crashed.

Example: "Hello World" Plugin in Go

This example demonstrates how to follow the contract, including the use of stdout and stderr.

hello/main.go:

``` 
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "os"
    "time"
)

// Metadata defines the structure for the --metadata flag.
type Metadata struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Usage       string `json:"usage"`
    Version     string `json:"version"`
}

// logf sends progress messages to the user (via stderr).
func logf(format string, v ...interface{}) {
    fmt.Fprintf(os.Stderr, format, v...)
}

func main() {
    // 1. Handle the --metadata flag
    metadataFlag := flag.Bool("metadata", false, "Displays the plugin metadata")
    flag.Parse()

    if *metadataFlag {
        meta := Metadata{
            Name:        "@hello",
            Description: "An example plugin that demonstrates stdout/stderr flow.",
            Usage:       "@hello [your-name]",
            Version:     "1.0.0",
        }
        jsonMeta, _ := json.Marshal(meta)
        fmt.Println(string(jsonMeta)) // Metadata goes to stdout
        return
    }

    // 2. Main plugin logic
    logf("🚀 Plugin 'hello' started!\n") // Progress log to stderr

    time.Sleep(2 * time.Second) // Simulate some work
    logf("   - Performing a time-consuming task...\n")
    time.Sleep(2 * time.Second)

    name := "World"
    if len(flag.Args()) > 0 {
        name = flag.Args()[0]
    }

    logf("✅ Task completed!\n") // More progress logs to stderr

    // 3. Send the final result to stdout
    // This is the only string that will be returned to chatcli as the result.
    fmt.Printf("Hello, %s! The current time is %s.", name, time.Now().Format(time.RFC1123))
}
```
#### Example Compilation and Installation

1. Compile the executable:
>go build -o hello ./hello/main.go

2. Move to the plugins directory:
>Create the directory if it doesn't exist:
mkdir -p ~/.chatcli/plugins/

3. Move the executable:
>mv hello ~/.chatcli/plugins/

You will see the progress logs (🚀 Plugin 'hello' started!...) in real time in your terminal, and at the end, the message Hello, World!... will be treated as the command output.

### Agent One-Shot Mode

Perfect for scripts and automation.

- Default Mode (Dry-Run): Only suggests the command and exits.
    - chatcli -p "/agent list all .go files in this directory"

- Automatic Execution Mode: Use the  --agent-auto-exec  flag to have the agent execute the first suggested command (dangerous commands are automatically blocked).
  - chatcli -p "/agent create a file named test_file.txt" --agent-auto-exec

--------

## Customizable Agents (Personas)

ChatCLI allows you to create **Customizable Agents** (also called Personas) that define specific behaviors for the AI. It's a modular system where:

- **Agents** define *"who"* the AI is (personality, specialization)
- **Skills** define *"what"* it should know/obey (rules, knowledge)

### Concept

An Agent can import multiple Skills, creating a composed **"Super System Prompt"**. This allows:

- Reusing knowledge across different agents
- Centralizing coding style rules, security, etc.
- Versioning personas in Git
- Sharing across teams
- **Syncing with the server**: When connecting to a remote server, server-side agents and skills are automatically discovered and merged with local ones
- **Dispatch as workers**: Custom agents are automatically registered in the multi-agent orchestration system and can be dispatched via `<agent_call>` by the LLM

### File Structure

Agents and skills are searched at two levels with **project precedence over global**:

```
~/.chatcli/                    # Global (fallback)
├── agents/
│   ├── go-expert.md
│   └── devops-senior.md
└── skills/
    ├── clean-code/            # V2 Skill (package)
    │   ├── SKILL.md
    │   └── scripts/
    │       └── lint_check.py
    ├── error-handling.md      # V1 Skill
    └── docker-master.md

my-project/                    # Project (priority)
├── .agent/
│   ├── agents/
│   │   └── backend.md         # Overrides global if same name
│   └── skills/
│       └── team-rules.md
└── ...
```

ChatCLI detects the project root by looking for `.agent/` or `.git/` from the current directory.

#### Agent Format

```yaml
---
name: "devops-senior"
description: "Senior DevOps with CI/CD and infrastructure focus"
tools: Read, Grep, Glob, Bash, Write, Edit   # Defines which tools the agent can use as a worker
skills:
  - clean-code
  - bash-linux
  - architecture
plugins:
  - "@coder"
---
# Base Personality

You are a Senior DevOps Engineer, specializing in CI/CD,
containers, infrastructure as code, and observability.
```

The `tools` field defines which commands the agent can use when dispatched as a worker in the multi-agent system:

| YAML Tool | @coder Command |
|-----------|----------------|
| `Read` | `read` |
| `Grep` | `search` |
| `Glob` | `tree` |
| `Bash` | `exec`, `test`, `git-*` |
| `Write` | `write` |
| `Edit` | `patch` |

Agents without `tools` defined are automatically read-only (`read`, `search`, `tree`).

#### Skill Format

```yaml
---
name: "clean-code"
description: "Clean Code Principles"
---
# Clean Code Rules

1. Use meaningful names for variables and functions
2. Keep functions small (max 20 lines)
3. Avoid unnecessary comments - code should be self-explanatory
```

V2 Skills (directories) can include subskills (.md) and executable scripts in `scripts/`. Scripts are automatically registered as executable skills in the worker and can be invoked during orchestration.

### Management Commands

| Command | Description |
|---------|-------------|
| `/agent list` | Lists all available agents |
| `/agent status` | Lists only attached agents (summary) |
| `/agent load <name>` | Loads a specific agent |
| `/agent attach <name>` | Attaches an additional agent to the session (combines skills) |
| `/agent detach <name>` | Removes an attached agent |
| `/agent skills` | Lists all available skills |
| `/agent show [--full]` | Shows the active agent (use --full for complete prompt) |
| `/agent off` | Deactivates the current agent |

### Practical Example

```bash
# 1. List available agents
/agent list

# 2. Load the devops-senior agent
/agent load devops-senior

# 3. Use in agent or coder mode
/agent configure the CI/CD pipeline with GitHub Actions
/coder create the multi-stage Dockerfile for production

# 4. The LLM can dispatch the agent as a worker automatically:
#    <agent_call agent="devops-senior" task="Set up CI/CD pipeline" />

# 5. Deactivate when done
/agent off
```

When an agent is loaded, all interactions with `/agent <task>` or `/coder <task>` will automatically use the loaded agent's persona. Additionally, **all custom agents are registered as workers** in the orchestration system — the LLM can dispatch them via `<agent_call>` with the same ReAct loop, parallel reads, and error recovery as built-in agents.

--------

## Remote Server Mode (gRPC)

ChatCLI can run as a gRPC server, allowing remote access from any terminal, Docker, or Kubernetes.

### `chatcli serve` — Start Server

```bash
chatcli serve                                    # port 50051, no auth
chatcli serve --port 8080 --token my-token       # custom port and auth
chatcli serve --tls-cert cert.pem --tls-key key.pem  # with TLS
```

### `chatcli connect` — Connect to Server

```bash
chatcli connect myserver:50051                          # basic
chatcli connect myserver:50051 --token my-token         # with auth
chatcli connect myserver:50051 --use-local-auth         # use local OAuth
chatcli connect myserver:50051 --provider OPENAI --llm-key sk-xxx  # your credentials
chatcli connect myserver:50051 -p "Explain K8s pods"    # remote one-shot
```

The full interactive mode works transparently over the remote connection: sessions, agent, coder, contexts — everything available.

#### Remote Resource Discovery

Upon connecting, the client automatically discovers server resources:

```
Connected to ChatCLI server (version: 1.3.0, provider: CLAUDEAI, model: claude-sonnet-4-5)
 Server has 3 plugins, 2 agents, 4 skills available
```

- **Remote plugins**: Executed on the server (`/plugin list` shows `[remote]`), with local download option
- **Remote agents/skills**: Transferred to the client for local prompt composition, allowing merge with local resources
- **Hybrid**: Local and remote plugins coexist; local and remote agents are merged automatically

### Docker

```bash
docker build -t chatcli .
docker run -p 50051:50051 -e LLM_PROVIDER=OPENAI -e OPENAI_API_KEY=sk-xxx chatcli
```

### Kubernetes (Helm)

```bash
# Basic
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx \
  --set server.token=my-token

# With multi-target watcher + Prometheus
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx \
  --set watcher.enabled=true \
  -f values-targets.yaml
```

The Helm chart supports `watcher.targets[]` for multi-target, Prometheus scraping, and auto-detects ClusterRole when targets span different namespaces.

#### Provisioning Agents, Skills, and Plugins via Helm

```bash
# With inline agents and skills
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set agents.enabled=true \
  --set-file agents.definitions.go-expert\\.md=agents/go-expert.md \
  --set skills.enabled=true \
  --set-file skills.definitions.clean-code\\.md=skills/clean-code.md

# With plugins via init container
helm install chatcli deploy/helm/chatcli \
  --set plugins.enabled=true \
  --set plugins.initImage=myregistry/chatcli-plugins:latest
```

Agents and skills are mounted as ConfigMaps at `/home/chatcli/.chatcli/agents/` and `/home/chatcli/.chatcli/skills/`. Plugins can come from an init container image or an existing PVC. Connected clients discover these resources automatically via gRPC.

> **gRPC and multiple replicas**: gRPC uses persistent HTTP/2 connections that pin to a single pod. For `replicaCount > 1`, enable `service.headless: true` in the Helm chart to activate round-robin load balancing via DNS. The Operator enables headless **automatically** when `spec.replicas > 1`. The client already has built-in keepalive and round-robin support.

> Full documentation at [diillson.github.io/chatcli/docs/getting-started/docker-deployment](https://diillson.github.io/chatcli/docs/getting-started/docker-deployment/)

--------

## Kubernetes Monitoring (K8s Watcher)

ChatCLI monitors **multiple deployments simultaneously**, collecting metrics, logs, events, pod status, and **Prometheus application metrics**. Use AI to diagnose issues with natural language questions.

### `chatcli watch` — Local Monitoring

```bash
# Single deployment (legacy)
chatcli watch --deployment myapp --namespace production

# Multiple deployments via config YAML
chatcli watch --config targets.yaml

# One-shot with multiple targets
chatcli watch --config targets.yaml -p "Which deployments need attention?"
```

### Config YAML (Multi-Target)

```yaml
interval: "30s"
window: "2h"
maxLogLines: 100
maxContextChars: 32000
targets:
  - deployment: api-gateway
    namespace: production
    metricsPort: 9090
    metricsFilter: ["http_requests_total", "http_request_duration_*"]
  - deployment: auth-service
    namespace: production
  - deployment: worker
    namespace: batch
```

### Integrated with Server

```bash
# Multi-target server (all clients receive context automatically)
chatcli serve --watch-config targets.yaml

# Or legacy single-target
chatcli serve --watch-deployment myapp --watch-namespace production
```

### What is Collected

- Pod status (restarts, OOMKills, CrashLoopBackOff)
- Kubernetes events (Warning, Normal)
- Recent logs from each container
- CPU/memory metrics (via metrics-server)
- **Prometheus application metrics** (from pod `/metrics` endpoints)
- Deployment rollout status, HPA and Ingress

### Context Budget Management

With multiple targets, the **MultiSummarizer** manages LLM context automatically: unhealthy targets get detailed context, healthy targets get compact one-liners, respecting the `maxContextChars` limit.

### K8s Operator — AIOps Platform

The **ChatCLI Operator** goes beyond instance management. It implements a **full autonomous AIOps platform** with 7 CRDs (`platform.chatcli.io/v1alpha1`):

| CRD | Description |
|-----|-------------|
| **Instance** | Manages ChatCLI server instances (Deployment, Service, RBAC, PVC) |
| **Anomaly** | Raw signal detected by K8s Watcher (restarts, OOM, deploy failures) |
| **Issue** | Correlated incident grouping multiple anomalies |
| **AIInsight** | AI-generated root cause analysis with enriched K8s context |
| **RemediationPlan** | Concrete actions to fix the issue (runbook-based or agentic AI-driven) |
| **Runbook** | Operational procedures (manual or AI auto-generated) |
| **PostMortem** | Auto-generated incident report after agentic resolution |

**Autonomous pipeline**: Detection → Correlation → AI Analysis (with K8s context) → Runbook-first → Remediation (including agentic mode) → Resolution → PostMortem

The AI receives full cluster context (deployment status, pods, events, revision history) and returns structured actions. In **agentic mode**, the AI acts as an agent with K8s skills — observing, deciding, and acting iteratively (observe-decide-act loop), saving history at each step. On resolution, it auto-generates a **PostMortem** (root cause, timeline, lessons learned) and a **reusable Runbook** for future incidents.

> Full documentation at [diillson.github.io/chatcli/docs/features/k8s-operator](https://diillson.github.io/chatcli/docs/features/k8s-operator/)
> AIOps deep-dive at [diillson.github.io/chatcli/docs/features/aiops-platform](https://diillson.github.io/chatcli/docs/features/aiops-platform/)

--------

## Code Structure and Technologies

The project has a modular structure organized into packages:

-  cli : Manages the interface and agent mode.
    -  cli/agent/workers : Multi-agent system with 12 specialized agents, async dispatcher, skills with accelerator scripts, and parallel orchestration.
-  config : Handles configuration via constants.
-  i18n : Centralizes internationalization logic and translation files.
-  llm : Manages communication and LLM client handling.
-  server : gRPC server for remote access (includes `GetAlerts`, `AnalyzeIssue`, and plugin/agent/skill discovery RPCs).
-  client/remote : gRPC client implementing the LLMClient interface, with remote resource discovery and usage support (plugins, agents, skills).
-  k8s : Kubernetes Watcher (collectors, store, summarizer).
-  proto : Protobuf service definitions (`chatcli.proto`).
-  operator : Kubernetes Operator — AIOps platform with 7 CRDs and autonomous pipeline.
    -  operator/api/v1alpha1 : CRD types (Instance, Anomaly, Issue, AIInsight, RemediationPlan, Runbook, PostMortem).
    -  operator/controllers : Reconcilers, correlation engine, WatcherBridge, gRPC client.
-  utils : Contains auxiliary functions for files, Git, shell, HTTP, etc.
-  models : Defines data structures.
-  version : Manages version information.

Key Go libraries used: Zap, go-prompt, Glamour, Lumberjack, Godotenv, golang.org/x/text, google.golang.org/grpc, k8s.io/client-go, controller-runtime.

--------

## Contributing

Contributions are welcome!

1. Fork the repository.
2. Create a new branch for your feature:  git checkout -b feature/my-new-feature .
3. Commit your changes and push to the remote repository.
4. Open a Pull Request.

--------

## License

This project is licensed under the MIT License.

--------

## Contact

For questions or support, please open an issue https://github.com/diillson/chatcli/issues on the repository.

--------

ChatCLI combines the power of LLMs with the simplicity of the command line, offering a versatile tool for continuous AI interactions directly in your terminal. Enjoy and transform your productivity experience! 🗨️✨
