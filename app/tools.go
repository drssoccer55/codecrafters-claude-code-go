package main

import (
	"github.com/openai/openai-go/v3"
)

type ReadRequest struct {
	FilePath string `json:"file_path"`
}

func READ_TOOL() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        "Read",
				Description: openai.String("Read and return the contents of a file"),
				Parameters: openai.FunctionParameters{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]string{
							"type":        "string",
							"description": "The path to the file to read",
						},
					},
					"required": []string{"file_path"},
				},
			},
		},
	}
}

type WriteRequest struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func WRITE_TOOL() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        "Write",
				Description: openai.String("Write content to a file"),
				Parameters: openai.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]string{
							"type":        "string",
							"description": "The path to the file to write to",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "The content to write to the file",
						},
					},
					"required": []string{"file_path", "content"},
				},
			},
		},
	}
}

type BashRequest struct {
	Command string `json:"command"`
}

func BASH_TOOL() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        "Bash",
				Description: openai.String("Execute a shell command"),
				Parameters: openai.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]string{
							"type":        "string",
							"description": "The command to execute",
						},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

type SkillRequest struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

func SKILL_TOOL() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        "Skill",
				Description: openai.String("Load a skill's instructions into the conversation"),
				Parameters: openai.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]string{
							"type":        "string",
							"description": "The name of the skill to use",
						},
						"args": map[string]string{
							"type":        "string",
							"description": "Optional arguments for the skill",
						},
					},
					"required": []string{"name"},
				},
			},
		},
	}
}

type LSPRequest struct {
	Operation string `json:"operation"`
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

func LSP_TOOL() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        "LSP",
				Description: openai.String("Returns the file, line, and character where a symbol is defined."),
				Parameters: openai.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"operation": map[string]any{
							"type": "string",
							"enum": []string{"goToDefinition", "findReferences"},
						},
						"filePath": map[string]string{
							"type":        "string",
							"description": "Absolute path, or a path relative to the agent's current working directory.",
						},
						"line": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"description": "1-based line number of the symbol.",
						},
						"character": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"description": "1-based character offset in UTF-16 code units (same as character count for ASCII).",
						},
					},
					"required": []string{"operation", "filePath", "line", "character"},
				},
			},
		},
	}
}
