<p align="center">
  <a href="https://ai.edilsonfreitas.com/">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

# Bringing Your Terminal Closer to Artificial Intelligence 🕵️‍♂️✨

**ChatCLI** is an advanced command-line application (CLI) that integrates powerful Large Language Models (LLMs) (such as OpenAI, StackSpot, GoogleAI, ClaudeAI, and xAI) to facilitate interactive and contextual conversations directly in your terminal. Designed for developers, data scientists, and tech enthusiasts, it enhances productivity by aggregating various contextual data sources and offering a rich, user-friendly experience.

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

-----

### 📝 Table of Contents

- [Key Features](https://www.google.com/search?q=%23key-features)
- [Installation](https://www.google.com/search?q=%23installation)
- [Configuration](https://www.google.com/search?q=%23configuration)
- [Usage and Commands](https://www.google.com/search?q=%23usage-and-commands)
    - [Interactive Mode](https://www.google.com/search?q=%23interactive-mode)
    - [Non-Interactive Mode (One-Shot)](https://www.google.com/search?q=%23non-interactive-mode-one-shot)
    - [CLI Commands](https://www.google.com/search?q=%23cli-commands)
- [Advanced File Processing](https://www.google.com/search?q=%23advanced-file-processing)
    - [Modes of `@file` Usage](https://www.google.com/search?q=%5Bhttps://www.google.com/search%3Fq%3D%2523modes-of-file-usage%5D\(https://www.google.com/search%3Fq%3D%2523modes-of-file-usage\))
    - [Chunking System in Detail](https://www.google.com/search?q=%23chunking-system-in-detail)
- [Agent Mode](https://www.google.com/search?q=%23agent-mode)
    - [Agent Interaction](https://www.google.com/search?q=%23agent-interaction)
    - [Agent One-Shot Mode](https://www.google.com/search?q=%23agent-one-shot-mode)
- [Code Structure and Technologies](https://www.google.com/search?q=%23code-structure-and-technologies)
- [Contributing](https://www.google.com/search?q=%23contributing)
- [License](https://www.google.com/search?q=%23license)
- [Contact](https://www.google.com/search?q=%23contact)

-----

## Key Features

- **Support for Multiple Providers**: Switch between OpenAI, StackSpot, ClaudeAI, GoogleAI, and xAI.
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

## Installation

### Prerequisites

- **Go (version 1.23+)**: [Available at golang.org](https://golang.org/dl/).

### Installation Steps

1.  **Clone the Repository**:
    ```bash
    git clone https://github.com/diillson/chatcli.git
    cd chatcli
    ```
2.  **Install Dependencies and Compile**:
    ```bash
    go mod tidy
    go build -o chatcli
    ```
    To compile with version information:
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
    This injects version data into the binary, accessible via `/version` or `chatcli --version`.

### Installation via `go install` (optional)

```bash
go install github.com/diillson/chatcli@latest
```

This will install the binary to your `$GOPATH/bin` folder, allowing you to run `chatcli` directly from your terminal if `$GOPATH/bin` is in your `PATH`.

-----

## Configuration

ChatCLI uses environment variables to define its behavior and connect to LLM providers. The easiest way is to create a `.env` file in the project's root directory.

### Essential Environment Variables

- **General**:
    - `CHATCLI_DOTENV` – **(Optional)** Defines the path to your `.env` file.
    - `LOG_LEVEL` (`debug`, `info`, `warn`, `error`)
    - `LLM_PROVIDER` (`OPENAI`, `STACKSPOT`, `CLAUDEAI`, `GOOGLEAI`, `XAI`)
    - `ENV` - **(Optional)** Defines how the log will be displayed (`dev`, `prod`). Default: `dev`.
      - `dev` displays the logs directly in the terminal and saves them to the log file.
      - `prod` only saves them to the log file, keeping the terminal cleaner.
- **Providers**:
    - `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_ASSISTANT_MODEL`, `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES`
    - `CLAUDEAI_API_KEY`, `CLAUDEAI_MODEL`, `CLAUDEAI_MAX_TOKENS`, `CLAUDEAI_API_VERSION`
    - `GOOGLEAI_API_KEY`, `GOOGLEAI_MODEL`, `GOOGLEAI_MAX_TOKENS`
    - `XAI_API_KEY`, `XAI_MODEL`, `XAI_MAX_TOKENS`
    - `CLIENT_ID`, `CLIENT_SECRET`, `SLUG_NAME`, `TENANT_NAME` (for StackSpot)

### Example `.env`

```env
# General Settings

LOG_LEVEL=info
ENV=prod
LLM_PROVIDER=CLAUDEAI
LOG_FILE=app.log
LOG_MAX_SIZE=300MB
HISTORY_MAX_SIZE=300MB

# OpenAI Settings
OPENAI_API_KEY=your-openai-key
OPENAI_MODEL=gpt-4o-mini
OPENAI_ASSISTANT_MODEL=gpt-4o-mini
OPENAI_USE_RESPONSES=true # use the Responses API (e.g., for gpt-5)
OPENAI_MAX_TOKENS=60000

# StackSpot Settings
CLIENT_ID=your-client-id
CLIENT_SECRET=your-client-secret
SLUG_NAME=your-stackspot-slug
TENANT_NAME=your-tenant-name

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
```

-----

## Usage and Commands

### Interactive Mode

Start the application with `./chatcli` and begin your conversation.

### Non-Interactive Mode (One-Shot)

Execute prompts in a single line, ideal for scripting and automation.

- **Quick examples**:
  ```bash
  chatcli -p "Quickly explain this repository."
  chatcli -p "@git @env Create a concise release note."
  chatcli -p "@file ./src --mode summary Provide an overview of the architecture."
  ```
- **Input via `stdin` (Pipes)**:
  ```bash
  git diff | chatcli -p "Summarize the changes and list potential impacts."
  ```
- **Available One-Shot Flags**:
    - `-p` or `--prompt`: The text to send to the LLM for a single execution.
    - `--provider`: Overrides the LLM provider at runtime (`OPENAI`, `OPENAI_ASSISTANT`, `CLAUDEAI`, `GOOGLEAI`, `STACKSPOT`, `XAI`).
    - `--model`: Chooses the model for the active provider (e.g., `gpt-4o-mini`, `claude-3-5-sonnet-20241022`, `gemini-2.5-flash`, etc.).
    - `--timeout`: Sets the timeout for the one-shot call (default: `5m`).
    - `--no-anim`: Disables animations (useful in scripts/CI).
    - `--agent-auto-exec`: Automatically executes the first command suggested by the agent (in agent mode).

Note: The same contextual features work within the `--prompt` text, such as `@file`, `@git`, `@env`, `@command`, and the `>` operator to add context. Remember to enclose the prompt in double quotes in the shell to avoid unwanted interpretations.

### CLI Commands

- **Session Management**:
    - `/session save <name>`, `/session load <name>`, `/session list`, `/session delete <name>`, `/session new`
- **Configuration and Status**:
    - `/switch`, `/reload`, `/config` or `/status` (displays runtime settings, current provider, and model).
- **General**:
    - `/help`: Displays help information.
    - `/exit`: To exit ChatCLI.
    - `/version` or `/v`: Shows the version, commit hash, and build date.
    - `Ctrl+C` (once): Cancels the current operation.
    - `Ctrl+C` (twice) or `Ctrl+D`: Exits the application.
- **Context**:
    - `@history`, `@git`, `@env`, `@file`, `@command`.

-----

## Advanced File Processing

The `@file <path>` command is the primary tool for sending files and directories, with support for path expansion (`~`).

### Modes of `@file` Usage

- **Default Mode (`full`)**: Processes the entire content of a file or directory, truncating it if the token limit is exceeded. Ideal for small to medium-sized projects.
- **Summary Mode (`summary`)**: Returns only the directory structure, file list with sizes, and general statistics. Useful for getting an overview without the content.
- **Smart Mode (`smart`)**: ChatCLI assigns a relevance score to each file based on your question and includes only the most pertinent ones.
  ```bash
  @file --mode smart ~/my-project/ How does the login system work?
  ```
- **Chunked Mode (`chunked`)**: For large projects, it splits the content into manageable chunks, sending one at a time.

### Chunking System in Detail

After the first chunk is sent, use `/nextchunk` to process the next. The system provides visual feedback on progress and the number of remaining chunks. To manage failures, use `/retry`, `/retryall`, or `/skipchunk`.

-----

## Agent Mode

**Agent Mode** allows the AI to interact with your system, suggesting or executing commands to automate complex or repetitive tasks.

### Agent Interaction

Start the agent with `/agent <query>` or `/run <query>`. The agent will suggest commands that you can approve or refine.

- **Refining**: Use `pCN` to add context before executing command `N`.
- **Adding context to the output**: After execution, use `aCN` to add information to the output of command `N` and get a new response from the AI.

### Agent One-Shot Mode

Perfect for scripts and automation.

- **Default Mode (Dry-Run)**: Only suggests the command and exits.
  ```bash
  chatcli -p "/agent list all .go files in this directory"
  ```
- **Automatic Execution Mode**: Use the `--agent-auto-exec` flag to have the agent execute the first suggested command (dangerous commands are automatically blocked).
  ```bash
  chatcli -p "/agent create a file named test_file.txt" --agent-auto-exec
  ```

-----

## Code Structure and Technologies

The project has a modular structure organized into packages:

- **`cli`**: Manages the interface and agent mode.
- **`config`**: Handles configuration via constants.
- **`llm`**: Manages communication and LLM client handling.
- **`utils`**: Contains auxiliary functions for files, Git, shell, HTTP, etc.
- **`models`**: Defines data structures.
- **`version`**: Manages version information.

Key Go libraries used: **Zap**, **go-prompt**, **Glamour**, **Lumberjack**, and **Godotenv**.

-----

## Contributing

Contributions are welcome\!

1.  **Fork the repository.**
2.  **Create a new branch for your feature:** `git checkout -b feature/my-new-feature`.
3.  **Commit your changes and push to the remote repository.**
4.  **Open a Pull Request.**

-----

## License

This project is licensed under the [MIT License](https://www.google.com/search?q=/LICENSE).

-----

## Contact

For questions or support, please open an [issue](https://www.google.com/search?q=https://github.com/diillson/chatcli/issues) on the repository.

-----

**ChatCLI** combines the power of LLMs with the simplicity of the command line, offering a versatile tool for continuous AI interactions directly in your terminal. Enjoy and transform your productivity experience! 🗨️✨