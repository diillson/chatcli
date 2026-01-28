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
  -  CLAUDEAI_API_KEY ,  CLAUDEAI_MODEL ,  CLAUDEAI_MAX_TOKENS ,  CLAUDEAI_API_VERSION
  -  GOOGLEAI_API_KEY ,  GOOGLEAI_MODEL ,  GOOGLEAI_MAX_TOKENS
  -  XAI_API_KEY ,  XAI_MODEL ,  XAI_MAX_TOKENS
  -  OLLAMA_ENABLED ,  OLLAMA_BASE_URL ,  OLLAMA_MODEL ,  OLLAMA_MAX_TOKENS ,  OLLAMA_FILTER_THINKING  – (Optional) Filters "thinking aloud" from models like Qwen3 (true/false, default: true)
  -  CLIENT_ID ,  CLIENT_KEY ,  STACKSPOT_REALM ,  STACKSPOT_AGENT_ID  (for StackSpot)
- Agent:
  -  `CHATCLI_AGENT_CMD_TIMEOUT`  – **(Optional)** Default timeout for each command executed from the action list by Agent Mode. Accepts Go durations (e.g., 30s, 2m, 10m). Default:  10m .
  -  `CHATCLI_AGENT_DENYLIST`  – **(Optional)** Semicolon-separated list of regular expressions to block extra dangerous commands. Example: rm\s+-rf\s+.;curl\s+[^|;]|\s*(sh|bash).
  -  `CHATCLI_AGENT_ALLOW_SUDO`  – **(Optional)** Allow sudo commands without automatic blocking (true/false). Default:  false  (sudo is blocked for safety).
  -  `CHATCLI_AGENT_PLUGIN_MAX_TURNS` - **(Optional)** Defines the maximum number of turns the agent can have. Default: 7.
  -  `CHATCLI_AGENT_PLUGIN_TIMEOUT` - **(Optional)** Defines the execution timeout for the agent plugin (e.g., 30s, 2m, 10m). Default: 15 (Minutes)

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

    Agent Settings
    CHATCLI_AGENT_CMD_TIMEOUT=2m   # The command will have 2m to run after that it is locked and finished
    CHATCLI_AGENT_DENYLIST=rm\\s+-rf\\s+.*;curl\\s+[^|;]*\\|\\s*(sh|bash);dd\\s+if=;mkfs\\w*\\s+
    CHATCLI_AGENT_ALLOW_SUDO=false
    CHATCLI_AGENT_PLUGIN_MAX_TURNS=10
    CHATCLI_AGENT_PLUGIN_TIMEOUT=20m
    
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
    CLAUDEAI_API_KEY=your-claudeai-key
    CLAUDEAI_MODEL=claude-3-5-sonnet-20241022
    CLAUDEAI_MAX_TOKENS=20000
    CLAUDEAI_API_VERSION=2023-06-01
    
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
  -  --model : Chooses the model for the active provider (e.g.,  gpt-4o-mini ,  claude-3-5-sonnet-20241022 ,  gemini-2.5-flash , etc.).
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
-General:
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

#### Security Policy

ChatCLI prioritizes safety by blocking dangerous commands by default. You can strengthen this policy with environment variables:

-  CHATCLI_AGENT_DENYLIST  to block additional patterns (regex, separated by semicolons " ; ").
-  CHATCLI_AGENT_ALLOW_SUDO  to allow/deny  sudo  without automatic blocking (default:  false ).
Even when allowed, dangerous commands may still require explicit confirmation in the terminal.

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

### File Structure

Files are stored in `~/.chatcli/`:

```
~/.chatcli/
├── agents/            # Agent files (.md)
│   ├── go-expert.md
│   └── devops-senior.md
└── skills/            # Skill files (.md)
    ├── clean-code.md
    ├── error-handling.md
    └── docker-master.md
```

#### Agent Format

```yaml
---
name: "go-expert"
description: "Go/Golang Specialist"
skills:
  - clean-code
  - error-handling
plugins:
  - "@coder"
---
# Base Personality

You are a Senior Software Engineer, specializing in Go.
Always prioritize simplicity and readability.
```

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

### Management Commands

| Command | Description |
|---------|-------------|
| `/agent list` | Lists all available agents |
| `/agent load <name>` | Loads a specific agent |
| `/agent skills` | Lists all available skills |
| `/agent show` | Shows the active agent and its prompt |
| `/agent off` | Deactivates the current agent |

### Practical Example

```bash
# 1. List available agents
/agent list

# 2. Load the go-expert agent
/agent load go-expert

# 3. Use in agent or coder mode
/agent create an HTTP server with graceful shutdown
/coder refactor this code to follow best practices

# 4. Deactivate when done
/agent off
```

When an agent is loaded, all interactions with `/agent <task>` or `/coder <task>` will automatically use the loaded agent's persona, applying its rules and specialized knowledge.

--------

## Code Structure and Technologies

The project has a modular structure organized into packages:

-  cli : Manages the interface and agent mode.
-  config : Handles configuration via constants.
-  i18n : Centralizes internationalization logic and translation files.
-  llm : Manages communication and LLM client handling.
-  utils : Contains auxiliary functions for files, Git, shell, HTTP, etc.
-  models : Defines data structures.
-  version : Manages version information.

Key Go libraries used: Zap, go-prompt, Glamour, Lumberjack, Godotenv, and golang.org/x/text.

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