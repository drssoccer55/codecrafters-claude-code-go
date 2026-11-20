package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type CommandLineColors struct {
	Reasoning *color.Color
	StdErr    *color.Color
	SubAgent  *color.Color
}

var cliColors = CommandLineColors{
	Reasoning: color.New(color.FgHiMagenta),
	StdErr:    color.New(color.FgRed),
	SubAgent:  color.New(color.FgGreen),
}

func main() {
	var prompt string
	flag.StringVar(&prompt, "p", "", "Prompt to send to LLM")
	flag.Parse()

	if prompt == "" {
		panic("Prompt must not be empty")
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	baseUrl := os.Getenv("OPENROUTER_BASE_URL")
	if baseUrl == "" {
		baseUrl = "https://openrouter.ai/api/v1"
	}

	if apiKey == "" {
		panic("Env variable OPENROUTER_API_KEY not found")
	}

	// go only has if / else no ternary operator
	model := "anthropic/claude-haiku-4.5"
	if v := os.Getenv("USE_FREE_MODEL"); v != "" {
		blue := color.New(color.FgHiBlue)
		blue.Fprintln(os.Stderr, "Using free nvidia model!")
		model = "nvidia/nemotron-3-ultra-550b-a55b:free"
	}

	client := openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseUrl))

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 100) // Initial buffer size 0 and capacity 100

	// Add the system prompt with accessible skills
	skills, err := get_skills()
	if err != nil {
		cliColors.StdErr.Fprintf(os.Stderr, "Could not load skills: %v\n", err)
	}
	if skills != nil {
		messages = append(messages, skills_to_system_prompt(skills))
	}

	// Add the initial prompt
	promptMessages, err := buildSkillMessages(prompt, skills)
	if err != nil {
		cliColors.StdErr.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	messages = append(messages, promptMessages...)

	tools := []openai.ChatCompletionToolUnionParam{READ_TOOL(), WRITE_TOOL(), BASH_TOOL(), SKILL_TOOL(), LSP_TOOL()}

	lspClient, err := LSPClientFromEnvironment()
	if err != nil {
		cliColors.StdErr.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	writeVersions := make(map[string]int)
	fileNotifVersions := make(map[string]map[string]struct{})

	agent := Agent{
		Client:           client,
		Messages:         messages,
		Tools:            tools,
		Model:            model,
		Skills:           skills,
		MaxIterations:    100,
		IsSubAgent:       false,
		LSPClient:        lspClient,
		WriteVersions:    writeVersions,
		Cursor:           0,
		ShownDiagnostics: fileNotifVersions,
	}

	output, err := run(agent)
	fmt.Println(output)
	if err != nil {
		cliColors.StdErr.Fprintf(os.Stderr, "Main agent failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
