# ChatCLI

![Lint & Test](https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg)
[![GitHub release](https://img.shields.io/github/v/release/diillson/chatcli)](https://github.com/diillson/chatcli/releases)
![GitHub issues](https://img.shields.io/github/issues/diillson/chatcli)
![GitHub last commit](https://img.shields.io/github/last-commit/diillson/chatcli)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/diillson/chatcli)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go%20Version)
![GitHub](https://img.shields.io/github/license/diillson/chatcli)

**ChatCLI** is an advanced command-line application (CLI) that integrates powerful Large Language Models (LLMs) like OpenAI, StackSpot, GoogleAI and ClaudeAI, making interactive and contextual conversations accessible directly from your terminal. Designed for developers, data scientists, and tech enthusiasts, ChatCLI supercharges productivity by combining contextual data sources and offering a rich, user-friendly experience.

---

## Table of Contents

* [Main Features](#main-features)
* [Installation](#installation)
* [Configuration](#configuration)
* [Usage and Commands](#usage-and-commands)

    * [Starting the Application](#starting-the-application)
    * [Non-interactive mode (one-shot via flags)](#non-interactive-mode-one-shot-via-flags)
    * [General Commands](#general-commands)
    * [Contextual Commands](#contextual-commands)
* [Advanced File Processing](#advanced-file-processing)

    * [Sending Files and Directories](#sending-files-and-directories)
    * [Usage Modes for `@file` Command](#usage-modes-for-file-command)
    * [Chunking System Details](#chunking-system-details)
* [Code Structure](#code-structure)
* [Libraries and Dependencies](#libraries-and-dependencies)
* [Logging Integration](#logging-integration)
* [Contributing](#contributing)
* [License](#license)
* [Contact](#contact)

---

## Main Features

* **Multi-Provider Support:** Seamlessly switch between StackSpot, OpenAI, and ClaudeAI as needed.
* **Interactive CLI Experience:** Command history navigation, auto-completion, and animated feedback (e.g., “Thinking...”).
* **Powerful Contextual Commands:**

    * `@history` – Inserts recent shell history (supports bash, zsh, and fish).
    * `@git` – Adds current Git repository info (status, commits, and branches).
    * `@env` – Injects environment variables into context.
    * `@file <path>` – Loads the contents of files or directories (supports `~` expansion and relative paths).
    * `@command <cmd>` – Runs system commands and adds their output to the context.
    * `@command --ai <cmd> > <context>` – Executes the command and sends the output directly to the LLM with extra context.
* **Recursive Directory Exploration:** Processes entire projects while ignoring irrelevant folders (e.g., `node_modules`, `.git`).
* **Dynamic Configuration & Persistent History:** Switch providers, update settings in real time, and maintain history between sessions.
* **Exponential Backoff Retry:** Robust error handling and recovery for external API communications.

---

## Installation

### Prerequisites

* **Go (version 1.23+)** – Download from [golang.org](https://golang.org/dl/).

### Installation Steps

1. **Clone the Repository:**

```bash
git clone https://github.com/diillson/chatcli.git
cd chatcli
```

2. **Install Dependencies:**

```bash
go mod tidy
```

3. **Build the Application:**

```bash
go build -o chatcli
```

4. **Run the Application:**

```bash
./chatcli
```

#### Building with Version Information

To compile the application with complete version information:

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
These flags inject version information into the binary, allowing the  /version  command to display accurate data.   

### Installation via Go Install (optional)
To install ChatCLI directly via Go, you can use the following command:

```bash
go install github.com/diillson/chatcli@latest
```
This will install ChatCLI in your `$GOPATH/bin` folder, allowing you to run the `chatcli` command directly in the terminal if your `$GOPATH/bin` is in your `$PATH`.

---

## Configuration

ChatCLI uses environment variables to control its behavior and connect to LLM providers. Set these via a `.env` file or directly in your shell.

### Environment Variables

* **.env Location:**

    * `CHATCLI_DOTENV` – (Optional) Path to your `.env` file.

* **General:**

    * `LOG_LEVEL` – (Optional) Levels: `debug`, `info`, `warn`, `error` (default: `info`).
    * `ENV` – (Optional) Environment: `prod` for production or `dev` for development (default: `dev`).
    * `LLM_PROVIDER` – (Optional) Default provider: `OPENAI`, `STACKSPOT`, or `CLAUDEAI` (default: `OPENAI`).
    * `LOG_FILE` – (Optional) Log file name (default: `app.log`).
    * `LOG_MAX_SIZE` – (Optional) Max log file size before rotation (default: `50MB`).
    * `HISTORY_MAX_SIZE` – (Optional) Max ChatCLI history size (default: `50MB`).

* **OpenAI Provider:**

    * `OPENAI_API_KEY` – OpenAI API key.
    * `OPENAI_MODEL` – (Optional) Model to use (default: `gpt-4o-mini`).
    * `OPENAI_ASSISTANT_MODEL` – (Optional) Model being used same as `OPENAI_MODEL` if set or (default: `gpt-4o-mini`).
    * `OPENAI_USE_RESPONSES` – (Optional) When `true`, use OpenAI Responses API for provider `OPENAI` (e.g., GPT‑5).
    * `OPENAI_MAX_TOKENS` – (Optional) Override of token limit used internally for chunking/truncation.

* **StackSpot Provider:**

    * `CLIENT_ID` – Client ID.
    * `CLIENT_SECRET` – Client secret.
    * `SLUG_NAME` – (Optional) Slug name (default: `testeai`).
    * `TENANT_NAME` – (Optional) Tenant name (default: `zup`).

* **ClaudeAI Provider:**

    * `CLAUDEAI_API_KEY` – ClaudeAI API key.
    * `CLAUDEAI_MODEL` – (Optional) Model (default: `claude-3-5-sonnet-20241022`).
    * `CLAUDEAI_MAX_TOKENS` – (Optional) Max tokens in the response (default: `8192`).
    * `CLAUDEAI_API_VERSION` – (Optional) Anthropic API version (default: `2023-06-01`).

* **Google AI Provider (Gemini)**:
    * `GOOGLEAI_API_KEY` – Google AI API key.
    * `GOOGLEAI_MODEL` – (Optional) Model to use (default: `gemini-2.0-flash-lite`)
    * `GOOGLEAI_MAX_TOKENS` – (Optional) Max tokens in response (default: `8192`).

### Example `.env` File

```env
# General Settings
LOG_LEVEL=info
ENV=dev
LLM_PROVIDER=CLAUDEAI
LOG_FILE=app.log
LOG_MAX_SIZE=300MB
HISTORY_MAX_SIZE=300MB

# OpenAI Settings
OPENAI_API_KEY=your-openai-key
OPENAI_MODEL=gpt-4o-mini
OPENAI_ASSISTANT_MODEL=gpt-4o-mini
OPENAI_USE_RESPONSES=true    # use Responses API (e.g., for gpt-5)
OPENAI_MAX_TOKENS=60000

# StackSpot Settings
CLIENT_ID=your-client-id
CLIENT_SECRET=your-client-secret
SLUG_NAME=your-slug-stackspot
TENANT_NAME=your-tenant-name

# ClaudeAI Settings
CLAUDEAI_API_KEY=your-claudeai-key
CLAUDEAI_MODEL=claude-3-5-sonnet-20241022
CLAUDEAI_MAX_TOKENS=20000
CLAUDEAI_API_VERSION=2023-06-01

# GoogleAI Settings (Gemini)
GOOGLEAI_API_KEY=sua-chave-googleai
GOOGLEAI_MODEL=gemini-2.5-flash
GOOGLEAI_MAX_TOKENS=20000
```

---

## Usage and Commands

Once installed and configured, ChatCLI offers a suite of commands for seamless LLM interaction.

### Starting the Application

- Interactive mode:
```bash
./chatcli
```

- Non-interactive (one-shot):
```bash
./chatcli -p "Your prompt here"
```

---

### Non-interactive mode (one-shot via flags)

ChatCLI now supports a “one-shot” mode to execute a single prompt on the command line and exit immediately. Perfect for scripts, CI/CD, aliases, and automation.

#### Available flags

- `-p` or `--prompt`: the text to send to the LLM for a one-time run.
- `--provider`: runtime override of the LLM provider (`OPENAI`, `CLAUDEAI`, `GOOGLEAI`, `OPENAI_ASSISTANT`, `STACKSPOT`).
- `--model`: choose the model for the selected provider (e.g., `gpt-4o-mini`, `claude-3-5-sonnet-20241022`, `gemini-2.5-flash`).
- `--timeout`: one-shot call timeout (default: `5m`).
- `--no-anim`: disable CLI animations (useful in scripts/CI).

Note: the same contextual features can be used inside the `--prompt` text, such as `@file`, `@git`, `@env`, `@command`, and the `>` operator for extra context. Always quote your prompt to avoid shell interpretation issues.

#### Quick examples

- Basic:
```bash
chatcli -p "Give a quick explanation of this repository."
```

- With contextual commands:
```bash
chatcli -p "@git @env Please craft a concise release note."
```
- Sending files/directories (existing  @file  modes apply):
```bash
chatcli -p "@file ./src --mode summary Provide an architecture overview."
```
- Override provider/model at runtime:
```bash
chatcli -p "Summarize the CHANGELOG" \
  --provider=CLAUDEAI \
  --model=claude-3-5-sonnet-20241022
```
- No animations (CI-friendly):
```bash
chatcli -p "What does this code do?" --no-anim
```
- Custom timeout:
```bash
chatcli -p "Give a detailed architecture analysis" --timeout=15m
```

### Stdin input (pipes)
In addition to `-p/--prompt`, ChatCLI accepts input via stdin in one-shot mode. This allows you to easily use pipes:

- Stdin only:
```bash
echo "Briefly explain this repository." | chatcli
```
- stdin + prompt (concatenates the two):
```bash
git diff | chatcli -p "Summarize the changes and list possible impacts."
or
echo "Briefly explain this repository." | chatcli -p
```
- With provider/model override:
```bash
cat README.md | chatcli \
  -p "Summarize the README and suggest improvements" \
  --provider=CLAUDEAI \
  --model=claude-3-5-sonnet-20241022
```
- No animations (CI-friendly):
```bash
echo "What does this code do?" | chatcli --no-anim
```

#### Tips and best practices

- Quoting: quote your prompt (especially if using  `>` for extra context).
- Pipes: no need to use  echo ... | chatcli  in one-shot mode; prefer  `-p ou -prompt` .
- Auto-detection: when `stdin` is not a TTY (e.g., via a pipe), ChatCLI runs in one-shot mode even without `-p/--prompt`.
- Combination: if both `-p/--prompt` and `stdin` are present, ChatCLI concatenates them by default (prompt content first, then `stdin` content).
- Output: Markdown-rendered output by default; consider  `--no-anim`  for strict parsers.
- Exit codes:  `0`  on success,  `1`  on runtime error,  `2`  on flag parsing errors.
- Script integration (Makefile):
```bash
one-shot:
    chatcli -p "@file ./ --mode summary Generate a README overview."
```
- Example (GitHub Actions):
```bash
- name: ChatCLI one-shot
  run: |
    chatcli -p "@file ./ --mode summary Generate a project overview"
```
---

### General Commands

* **End Session:**

    * `/exit`, `exit`, `/quit` or `quit`

* **Switch Provider or Settings:**

    * `/switch` – Interactive LLM provider switcher.
    * `/switch --model <model-name>`  – Switches the model for the current provider (e.g.,  `gpt-4o-mini` ,  `claude-3-5-sonnet-20241022` ).
    * `/switch --slugname <slug>` – Update only the `slugName`.
    * `/switch --tenantname <tenant>` – Update only the `tenantName`.
    * Combine: `/switch --slugname <slug> --tenantname <tenant>`
    * `/reload` – Reload environment variables in real time.
    * `/config` or `/status` (or `/settings`) – Display current ChatCLI configuration.
    - Shows: current provider and model (runtime), client-reported model name, preferred API (catalog), effective MaxTokens (estimated), token overrides from ENV, `.env` path, available providers, and (when applicable) StackSpot `slugName`/`tenantName`.
    - Security: never prints secret values (e.g., API keys). Instead shows presence as `[SET]`/`[NOT SET]` and sends nothing to the LLM.
    - Example:
         ```
         /config
         ```
         Expected summary:
         - Current provider: OPENAI
         - Current model: gpt-4o-mini (client: GPT-4o mini)
         - Preferred API: chat_completions
         - Effective MaxTokens: 50000

  * **Session Management:**
      * `/session save <name>` – Saves the current conversation with a given name.
      * `/session load <name>` – Loads a previously saved conversation.
      * `/session list` – Lists all saved sessions.
      * `/session delete <name>` – Deletes a saved session.
      * `/session new` or `/newsession` – Clears the current history and starts a new conversation session.

  * **Check Version and Updates:**
      * `/version` or `/v` – Shows current version, commit hash, and checks for available updates.
      * **Usage**: Useful to confirm which version is installed and if there are new versions available.
      * **Alternative**: Run `chatcli --version` or `chatcli -v` directly from the terminal.

  * Canceling In-Progress Operations:
      * `Ctrl+C`  (once): Cancels the current operation (e.g., waiting for the AI's response, the "Thinking..." animation) without exiting ChatCLI. You will return to the prompt.
      * `Ctrl+C`  (twice quickly) or  `Ctrl+D` : Exits the application.

* **Help:**

    * `/help`

### Contextual Commands

* `@history` – Inserts the last 10 shell commands.
* `@git` – Includes Git repository info.
* `@env` – Adds environment variables to the context.
* `@file <path>` – Inserts the content of a file or directory.
* `@command <cmd>` – Runs a terminal command and saves the output.
* `@command --ai <cmd> > <context>` – Sends the command output straight to the LLM with extra context.
* * Note: sensitive variables and command outputs are sanitized (tokens/secrets are redacted) before sending to the LLM.

---

### Agent Mode

Agent Mode allows the AI to execute tasks on your system via terminal commands:

* `/agent <request>` or `/run <request>` – Launch agent mode for a specific task.
* The agent will analyze your request and suggest appropriate commands.
* You can pick which commands to run or execute all suggested commands.
* **Usage Examples:**

```bash
  "/agent" List all PDF files in the current directory
  "/run" Create a compressed backup of the src/ folder
  "/agent" Which processes are using the most memory?
```

* The agent can handle complex operations such as file listing, backups, process checks, and more.
* Interact with the agent by providing feedback or requesting adjustments to the suggested tasks.
* Agent Mode is perfect for automating repetitive or complex tasks, letting you focus on what matters most.
* The agent keeps a history of executed commands so you can review actions and results.
* Agent Mode is designed for safety, respecting system permissions and ensuring only authorized commands are run.
* You can exit Agent Mode at any time, returning to normal chat.

#### Refining Commands Before Execution

You can now ask the AI to refine a suggested command before you run it by providing additional context.

- `pCN`  (Pre-Context for command N): Use this option to add instructions before execution.

##### Refinement Example:

1. The AI suggests command #1:  `ls -la`
2. You type:  `pC1`
3. You add your context:  Actually, I only want to see .go files and count the lines in each.
4. The AI will process your request and suggest a new, refined command, such as  `find . -name "*.go" -exec wc -l {} +` .

#### Adding Context to Outputs in Agent Mode!!

* You can now add context to outputs of agent-executed commands.

When using the new `aCN` feature, you can:

1. Execute a command (e.g., `1` to run command #1)
2. View the command output
3. Type `aC1` to add context to command #1
4. Add your notes, extra info, or questions (end with a `.` on a blank line)
5. The AI will reply based on the command, output, and your additional context

#### Example:

```text

📋 Command Output:
---------------------------------------
🚀 Running commands (type: shell):
---------------------------------------
⌛ Processing: List files

⚙️ Command 1/1: ls -la
📝 Command output (stdout/stderr):
total 24
drwxr-xr-x  5 user  staff   160 May 15 10:23 .
drwxr-xr-x  3 user  staff    96 May 15 10:22 ..
-rw-r--r--  1 user  staff  2489 May 15 10:23 main.go
-rw-r--r--  1 user  staff   217 May 15 10:23 go.mod
-rw-r--r--  1 user  staff   358 May 15 10:23 go.sum
✓ Successfully executed

---------------------------------------
Execution complete.
---------------------------------------

You: aC1
Type your additional context (finish with a line containing only '.') or press Enter to continue:
I need a script that lists only .go files in this directory
and counts how many lines each one has.
.

[The AI will then reply with an explanation and a new command to meet your specific need]
```

---

## Advanced File Processing

ChatCLI includes a robust system for uploading and processing files/directories, with modes tailored for anything from quick analyses to in-depth project exploration.

### Sending Files and Directories

To send a file or directory, use the `@file` command followed by the desired path. The command supports:

* **Path Expansion:**

    * `~` expands to your home directory.
    * Supports both relative (`./src/utils.js`) and absolute (`/usr/local/etc/config.json`) paths.

**Examples:**

* Send a specific file:

  ```
  You: @file ~/documents/main.go
  ```

* Send a complete directory:

  ```
  You: @file ~/projects/my-application/
  ```

---

### Usage Modes for `@file` Command

The `@file` command offers multiple modes to fit your needs:

1. **Default Mode (Full)**

    * **Best for:** Small to medium projects.
    * **How it works:**

        * Scans the directory and includes file contents up to the model's token limits.
        * May truncate content if token limits are exceeded.

2. **Chunked Mode (Divided)**

    * **Best for:** Large projects that need splitting into smaller parts.
    * **How it works:**

        * Splits content into manageable “chunks.”
        * Sends only the first chunk at first and stores the rest.
        * Use `/nextchunk` to manually load the next chunk.
    * **Example:**

      ```
      You: @file --mode chunked ~/my-large-project/
      ```

      After sending the first chunk, you’ll see:

      ```
      📊 PROJECT SPLIT INTO CHUNKS
      =============================
      ▶️ Total chunks: 5
      ▶️ Estimated files: ~42
      ▶️ Total size: 1.75 MB
      ▶️ You’re on chunk 1/5
      ▶️ Use '/nextchunk' to load the next chunk
      =============================
      ```

3. **Summary Mode**

    * **Best for:** Quick project overviews without file contents.
    * **How it works:**

        * Returns info on directory structure, file lists with sizes/types, and general stats.
    * **Example:**

      ```
      You: @file --mode summary ~/my-project/
      ```

4. **Smart Mode**

    * **Best for:** Targeted analysis, where you provide a question and the system selects the most relevant files.
    * **How it works:**

        * ChatCLI assigns relevance scores to each file based on your question, including only the most pertinent ones.
    * **Example:**

      ```
      You: @file --mode smart ~/my-project/ How does the login system work?
      ```

---

### Chunking System Details

For large projects using `chunked` mode:

1. **Chunk Initialization:**

    * ChatCLI scans the entire directory and splits contents into multiple chunks.
    * Each chunk gets metadata (e.g., chunk number, total chunks).
    * Only the first chunk is sent immediately; the rest are queued.

2. **Chunk Navigation:**

    * After receiving the first chunk, use `/nextchunk` to send the next one.
    * The system updates progress and shows remaining chunks.

3. **Failure Handling:**

    * If a chunk fails, it's listed separately.
    * Commands for chunk management:

        * `/retry` – Retry the last failed chunk.
        * `/retryall` – Retry all failed chunks.
        * `/skipchunk` – Skip a problematic chunk and continue.
        * `/nextchunk` – Move to the next chunk and keep the flow going.

4. **Visual Feedback:**

    * Each sent chunk includes a detailed header with progress info, like:

      ```
      📊 PROGRESS: Chunk 3/5
      =============================
      ▶️ 2 chunks processed
      ▶️ 2 chunks remaining
      ▶️ 1 chunk failed
      ▶️ Use '/nextchunk' to continue after this chunk
      =============================
      ```

---

## Code Structure

The project is split into packages with clear responsibilities:

* **`cli`**: Manages user interface.

    * `ChatCLI`: Main interaction loop.
    * `CommandHandler`: Handles special commands (e.g., `/exit`, `/switch`).
    * `HistoryManager`: Manages command history across sessions.
    * `AnimationManager`: Controls visual animations during processing.
    * `AgentMode`: Implements agent mode for command execution.
* **`llm`**: Handles communication with LLM providers.

    * `LLMClient`: Interface for LLM clients.
    * `OpenAIClient`, `StackSpotClient`, `ClaudeAIClient`: Specific provider clients.
    * `LLMManager`: Manages LLM clients.
    * `token_manager.go`: Handles tokens and renewals.
* **`utils`**: Helper functions.

    * `file_utils.go`: File/directory processing.
    * `shell_utils.go`: Shell interaction and history.
    * `git_utils.go`: Git info handling.
    * `http_client.go`, `logging_transport.go`: HTTP clients with logging.
    * `path.go`: Path manipulation.
* **`models`**: Data structures (e.g., `Message`, `ResponseData`).
* **`main`**: App initialization and dependency configuration.

---

## Libraries and Dependencies

* [Zap](https://github.com/uber-go/zap) – High-performance structured logging.
* [Liner](https://github.com/peterh/liner) – Command-line editing and history.
* [Glamour](https://github.com/charmbracelet/glamour) – Markdown rendering in the terminal.
* [Lumberjack](https://github.com/natefinch/lumberjack) – Log file rotation.
* [Godotenv](https://github.com/joho/godotenv) – Loads environment variables from .env files.
* [Go Standard Library](https://pkg.go.dev/std) – For HTTP, file handling, and concurrency.

---

## Logging Integration

ChatCLI leverages Zap for robust, structured logging, with:

* **Configurable Levels:** (`debug`, `info`, `warn`, `error`)
* **Log Rotation:** Managed by Lumberjack.
* **Sensitive Data Sanitization:** API keys, tokens, and critical data are redacted.
* **Multi-Output:** Logs are shown in the console and saved to file.
* **Request Details:** Complete info on methods, URLs, headers (with sensitive data removed), and response times.

---

## Contributing

Contributions are always welcome! To get started:

1. **Fork the Repository.**

2. **Create a New Branch:**

   ```bash
   git checkout -b feature/YourFeatureName
   ```

3. **Commit Your Changes:**

   ```bash
   git commit -m "Description of your change"
   ```

4. **Push the Branch to Remote:**

   ```bash
   git push origin feature/YourFeatureName
   ```

5. **Open a Pull Request.**

Please follow the project standards and make sure all tests pass.

---

## License

This project is licensed under the [MIT License](/LICENSE).

---

## Contact

For questions, suggestions, or support, open an issue in the repository or visit:
[www.edilsonfreitas.com.br/contact](https://www.edilsonfreitas.com/#section-contact)

---

**ChatCLI** merges LLM power with CLI simplicity, offering a versatile tool for seamless AI interactions right from your terminal. Enjoy and transform your productivity experience!

Happy chatting! 🗨️✨

---