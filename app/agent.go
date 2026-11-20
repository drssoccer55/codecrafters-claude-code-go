package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"
)

type Agent struct {
	Client           openai.Client
	Messages         []openai.ChatCompletionMessageParamUnion
	Tools            []openai.ChatCompletionToolUnionParam
	Model            string
	Skills           map[string]*Document
	MaxIterations    int
	IsSubAgent       bool
	LSPClient        *LSPClient
	WriteVersions    map[string]int //using absolute uri here
	Cursor           int
	ShownDiagnostics map[string]map[string]struct{}
}

func run(agent Agent) (string, error) {
	for i := 0; i < agent.MaxIterations; i++ {
		resp, err := agent.Client.Chat.Completions.New(context.Background(),
			openai.ChatCompletionNewParams{
				Model:    agent.Model,
				Messages: agent.Messages,
				Tools:    agent.Tools,
			},
		)
		if err != nil {
			return "Failed to call openrouter", fmt.Errorf("error: %v\n", err)
		}
		if len(resp.Choices) == 0 {
			panic("No choices in response")
		}

		// assume follow the first choice for now
		choice := resp.Choices[0]
		if choice.FinishReason == "tool_calls" {
			// if the finish reason is a tool call we need to make the tool call and give that info back to the llm
			agent.Messages = append(agent.Messages, choice.Message.ToParam()) // First append the message the llm game to itself
			msg := choice.Message
			if reasoning, ok := msg.JSON.ExtraFields["reasoning"]; ok && reasoning.Valid() {
				cliColors.Reasoning.Fprintf(os.Stderr, "\n--- REASONING ---\n%s\n", reasoning.Raw())
			}
			for _, call := range msg.ToolCalls {
				toolMsg := handleToolCall(call, agent)
				if agent.LSPClient != nil && !agent.IsSubAgent {
					notifResponse, err := getDiagnostics(agent.LSPClient, agent.Cursor)
					if err == nil {
						diagMsg, hasChange := getLSPNotificationsMsg(notifResponse, &agent)
						if hasChange {
							cliColors.StdErr.Fprintf(os.Stderr, "%s\n", diagMsg)
							toolMsg = fmt.Sprintf("%s\n\n%s", toolMsg, diagMsg)
						}
					}
				}
				agent.Messages = append(agent.Messages, toolMessage(toolMsg, call.ID))
			}
		} else {
			return choice.Message.Content, nil
		}
	}
	return "Exceeded agent max iterations", fmt.Errorf("Exceeded agent max iterations")
}

func getLSPNotificationsMsg(notifications *LSPNotifResponse, agent *Agent) (string, bool) {
	// Only publishDiagnostics notifications carry reports. Versioned reports must match
	// the last version sent for that file; reports without a version (decoded as 0) are
	// accepted for any file.
	// The latest current report replaces the previous one for that file, and an empty
	// report clears it. Only issues absent from the last report shown are appended.
	agent.Cursor = notifications.Cursor
	var body strings.Builder
	for _, fileNotif := range notifications.Notifications {
		if fileNotif.Params.Version != 0 {
			curFileVersion, ok := agent.WriteVersions[fileNotif.Params.URI]
			if !ok || curFileVersion != fileNotif.Params.Version {
				continue
			}
		}
		oldNotifs := agent.ShownDiagnostics[fileNotif.Params.URI]
		newNotifs := make(map[string]struct{})
		allNotifs := make(map[string]struct{}, len(fileNotif.Params.Diagnostics))
		for _, diag := range fileNotif.Params.Diagnostics {
			diagMsg := diagMsgStr(diag)
			allNotifs[diagMsg] = struct{}{}
			if _, inOld := oldNotifs[diagMsg]; !inOld {
				newNotifs[diagMsg] = struct{}{}
			}
		}
		agent.ShownDiagnostics[fileNotif.Params.URI] = allNotifs // always update!
		if len(newNotifs) == 0 {
			continue
		}
		relPath, err := URIToRelativePath(fileNotif.Params.URI)
		if err != nil {
			continue
		}
		keys := slices.Collect(maps.Keys(newNotifs))
		slices.Sort(keys)
		body.WriteString(fmt.Sprintf("%s:\n", relPath))
		for _, key := range keys {
			body.WriteString(fmt.Sprintf("  %s\n", key))
		}
		body.WriteString("\n")
	}
	if body.Len() == 0 {
		return "", false
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<new-diagnostics>The following new diagnostic issues were detected:\n\n")
	sb.WriteString(body.String())
	sb.WriteString("</new-diagnostics>")
	return sb.String(), true
}

func diagMsgStr(diag Diagnostic) string {
	return fmt.Sprintf("[Line %d:%d] %s", diag.Range.Start.Line+1, diag.Range.Start.Character+1, diag.Message)
}

var extensionPattern = regexp.MustCompile(`(\.[\w\d]+)`)

func FileURI(path string) (string, error) {
	// Convert relative path to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	// Convert absolute path to file URI
	u := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(absPath),
	}

	return u.String(), nil
}

func URIToRelativePath(uri string) (string, error) {
	// Parse the URI
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}

	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme: %s", u.Scheme)
	}

	// Convert URI path to filesystem path
	absPath := filepath.FromSlash(u.Path)

	// Resolve working directory
	absWorkingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Convert absolute path to relative path
	relPath, err := filepath.Rel(absWorkingDir, absPath)
	if err != nil {
		return "", err
	}

	return relPath, nil
}

func lspOpenFile(agent Agent, fileUri string, langId string, dat string) (json.RawMessage, error) {
	cliColors.StdErr.Fprintf(os.Stderr, "LSP open: %s\n", fileUri)
	return agent.LSPClient.Call("notify", map[string]any{
		"method": "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        fileUri,
				"languageId": langId,
				"version":    1, // when opening file the version starts as 1
				"text":       string(dat),
			},
		},
	})
}

func lspUpdateFile(agent Agent, fileUri string, version int, dat string) (json.RawMessage, error) {
	cliColors.StdErr.Fprintf(os.Stderr, "LSP version update for: %s %d\n", fileUri, version)
	return agent.LSPClient.Call("notify", map[string]any{
		"method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":     fileUri,
				"version": version,
			},
			"contentChanges": []map[string]any{{
				"text": dat,
			}},
		},
	})
}

// handleToolCall executes a single tool call and returns the content to send
// back to the model. Failures are returned as the tool result so the model can
// recover instead of terminating the session.
func handleToolCall(call openai.ChatCompletionMessageToolCallUnion, agent Agent) string {
	switch call.Function.Name {
	case "Read":
		data := ReadRequest{}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &data); err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "Read Tool! Could not unmarshall arguments to expected ReadRequest: %v\n", err)
			return fmt.Sprintf("Read Tool! Could not unmarshall arguments to expected ReadRequest: %v", err)
		}
		dat, err := os.ReadFile(data.FilePath)
		if err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "Read Tool! File not found error: %v\n", err)
			return fmt.Sprintf("Read Tool! Could not read %s: %v", data.FilePath, err)
		}
		cliColors.StdErr.Fprintf(os.Stderr, "Read: %s\n", data.FilePath)
		return string(dat)
	case "Write":
		data := WriteRequest{}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &data); err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "Write Tool! Could not unmarshall arguments to expected WriteRequest: %v\n", err)
			return fmt.Sprintf("Write Tool! Could not unmarshall arguments to expected WriteRequest: %v", err)
		}
		// check if file has extension supported by the lang server, if so handle version updates
		var shouldUpdateFileVersion = false
		var fileVersion = 1
		var langId = ""
		var fileUri = ""
		extension := extensionPattern.FindString(data.FilePath)
		if len(extension) != 0 && agent.LSPClient != nil { // have to gate lspclients that are not initialized in tests
			lang, ok := agent.LSPClient.ExtensionToLanguage[extension]
			if ok {
				langId = lang
				shouldUpdateFileVersion = true
				file, err := FileURI(data.FilePath)
				if err != nil {
					cliColors.StdErr.Fprintf(os.Stderr, "Write Tool! Could not convert relative path to absolute uri: %v\n", err)
					return fmt.Sprintf("Write Tool! Could not convert relative path to absolute uri: %s: %v", data.FilePath, err)
				}
				fileUri = file
				version, ok := agent.WriteVersions[fileUri]
				if ok {
					fileVersion = version + 1
				}
			}
		}

		if err := os.WriteFile(data.FilePath, []byte(data.Content), 0666); err != nil { // 0666 creates a file that can be read and wrote to
			cliColors.StdErr.Fprintf(os.Stderr, "Write Tool! %v\n", err)
			return fmt.Sprintf("Write Tool! Could not write %s: %v", data.FilePath, err)
		}
		cliColors.StdErr.Fprintf(os.Stderr, "Wrote to: %s\n", data.FilePath)

		// Now notify LSP server about updated file
		if shouldUpdateFileVersion {
			if fileVersion == 1 {
				_, err := lspOpenFile(agent, fileUri, langId, data.Content)
				if err != nil {
					return fmt.Sprintf("Write Tool! LSPClient textDocument/didOpen failed with: %v", err)
				}
			} else {
				_, err := lspUpdateFile(agent, fileUri, fileVersion, data.Content)
				if err != nil {
					return fmt.Sprintf("Write Tool! LSPClient textDocument/didChange failed with: %v", err)
				}
			}
			agent.WriteVersions[fileUri] = fileVersion
		}
		return "write successful"
	case "Bash":
		data := BashRequest{}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &data); err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "Bash Tool! Could not unmarshall arguments to expected BashRequest: %v\n", err)
			return fmt.Sprintf("Bash Tool! Could not unmarshall arguments to expected BashRequest: %v", err)
		}
		cmd := exec.Command("sh", "-c", data.Command)
		out, err := cmd.CombinedOutput()

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			cliColors.StdErr.Fprintf(os.Stderr, "Bash command interpreted as error exit: %s\n", data.Command)
			return fmt.Sprintf("Exited with code %d. Stderr: %s", exitErr.ExitCode(), err.Error())
		}
		cliColors.StdErr.Fprintf(os.Stderr, "Bash command executed: %s\n", data.Command)
		cliColors.StdErr.Fprintf(os.Stderr, "Command output: %s\n", string(out))
		return string(out)
	case "Skill":
		data := SkillRequest{}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &data); err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "Skill Tool! Could not unmarshall arguments to expected SkillRequest: %v\n", err)
			return fmt.Sprintf("Skill Tool! Could not unmarshall arguments to expected SkillRequest: %v", err)
		}
		cliColors.StdErr.Fprintf(os.Stderr, "Skill tool invoked: %s with args: %s\n", data.Name, data.Args)
		msgs, err := buildSkillMessages(fmt.Sprintf("/%s %s", data.Name, data.Args), agent.Skills)
		if err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "Skill Tool! Failed to build skill message: %v\n", err)
			return fmt.Sprintf("Skill tool failed for %s with args %s: %v", data.Name, data.Args, err)
		}
		if len(msgs) != 1 {
			cliColors.StdErr.Fprintf(os.Stderr, "Skill Tool! Expected one message, got %d\n", len(msgs))
			return fmt.Sprintf("Skill tool failed for %s with args %s: expected one message, got %d", data.Name, data.Args, len(msgs))
		}
		content := msgs[0].OfUser.Content.OfString.Value
		cliColors.StdErr.Fprintf(os.Stderr, "Skill tool success: %s\n", content)

		// Either call skill in subagent or main context
		doc, ok := agent.Skills[data.Name]
		if ok && strings.EqualFold(doc.Context, "fork") && !agent.IsSubAgent {
			cliColors.SubAgent.Fprint(os.Stderr, "Subagent created!\n")
			messages := make([]openai.ChatCompletionMessageParamUnion, 0, 10)
			messages = append(messages, userMessage(content))
			subAgent := Agent{
				Client:           agent.Client,
				Messages:         messages,
				Tools:            agent.Tools,
				Model:            agent.Model,
				Skills:           agent.Skills,
				MaxIterations:    10,
				IsSubAgent:       true,
				LSPClient:        agent.LSPClient,
				WriteVersions:    agent.WriteVersions,
				Cursor:           0,
				ShownDiagnostics: agent.ShownDiagnostics,
			}
			subAgentOut, err := run(subAgent)
			if err != nil {
				return fmt.Sprintf("Subagent failed with error: %s", err.Error())
			} else {
				cliColors.SubAgent.Fprintf(os.Stderr, "Subagent output: %s\n", subAgentOut)
				return subAgentOut
			}
		} else {
			return content
		}
	case "LSP":
		data := LSPRequest{}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &data); err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "LSP Tool! Could not unmarshall arguments to expected LSPRequest: %v\n", err)
			return fmt.Sprintf("LSP Tool! Could not unmarshall arguments to expected LSPRequest: %v", err)
		}
		dat, err := os.ReadFile(data.FilePath)
		if err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "LSP Tool! File not found error: %v\n", err)
			return fmt.Sprintf("LSP Tool! Could not read %s: %v", data.FilePath, err)
		}
		extension := extensionPattern.FindString(data.FilePath)
		if len(extension) == 0 {
			return fmt.Sprintf("LSP Tool! Could not identify extension for file %s", data.FilePath)
		}
		langId, ok := agent.LSPClient.ExtensionToLanguage[extension]
		if !ok {
			return fmt.Sprintf("LSP Tool! Identified file extension as %s but failed to map to languageId", extension)
		}
		fileUri, err := FileURI(data.FilePath)
		if err != nil {
			cliColors.StdErr.Fprintf(os.Stderr, "LSP Tool! Could not convert relative path to absolute uri: %v\n", err)
			return fmt.Sprintf("LSP Tool! Could not convert relative path to absolute uri: %s: %v", data.FilePath, err)
		}
		if _, ok := agent.WriteVersions[fileUri]; !ok {
			_, err = lspOpenFile(agent, fileUri, langId, string(dat))
			if err != nil {
				return fmt.Sprintf("LSP Tool! LSPClient textDocument/didOpen failed with: %v", err)
			}
			agent.WriteVersions[fileUri] = 1
		}
		if strings.EqualFold(data.Operation, "goToDefinition") {
			loc, err := agent.LSPClient.Call("request", map[string]any{
				"method": "textDocument/definition",
				"params": map[string]any{
					"textDocument": map[string]string{
						"uri": fileUri,
					},
					"position": map[string]int{
						"line":      data.Line - 1, // Convert to 0-based
						"character": data.Character - 1,
					},
				},
			})
			if err != nil {
				return fmt.Sprintf("LSP Tool! LSPClient textDocument/definition failed with: %v", err)
			}
			var locData LSPDefResponse
			if err := json.Unmarshal(loc, &locData); err != nil {
				return fmt.Sprintf("LSP Tool! LSPDefResponse could not be unmarshaled: %v", err)
			}
			relPath, err := URIToRelativePath(locData.URI)
			if err != nil {
				cliColors.StdErr.Fprintf(os.Stderr, "LSP Tool! Could not convert absolute uri to relative path: %v\n", err)
				return fmt.Sprintf("LSP Tool! Could not convert absolute uri to relative path: %s: %v", locData.URI, err)
			}
			// Convert back to 1-based
			return fmt.Sprintf("Defined in %s:%d:%d", relPath, locData.Range.Start.Line+1, locData.Range.Start.Character+1)
		} else if strings.EqualFold(data.Operation, "findReferences") {
			refs, err := agent.LSPClient.Call("request", map[string]any{
				"method": "textDocument/references",
				"params": map[string]any{
					"textDocument": map[string]string{
						"uri": fileUri,
					},
					"position": map[string]int{
						"line":      data.Line - 1, // Convert to 0-based
						"character": data.Character - 1,
					},
					"context": map[string]bool{
						"includeDeclaration": true,
					},
				},
			})
			if err != nil {
				return fmt.Sprintf("LSP Tool! LSPClient textDocument/references failed with: %v", err)
			}
			var refData []LSPDefResponse
			if err := json.Unmarshal(refs, &refData); err != nil {
				return fmt.Sprintf("LSP Tool! LSPDefResponse could not be unmarshaled: %v", err)
			}

			uniqueFiles := make(map[string][]string)
			for _, ref := range refData {
				relPath, err := URIToRelativePath(ref.URI)
				if err != nil {
					cliColors.StdErr.Fprintf(os.Stderr, "LSP Tool! Could not convert absolute uri to relative path: %v\n", err)
					return fmt.Sprintf("LSP Tool! Could not convert absolute uri to relative path: %s: %v", ref.URI, err)
				}
				existingArr, ok := uniqueFiles[relPath]
				if !ok {
					uniqueFiles[relPath] = make([]string, 0)
					existingArr = uniqueFiles[relPath]
				}

				existingArr = append(existingArr, fmt.Sprintf("Line %d:%d", ref.Range.Start.Line+1, ref.Range.Start.Character+1))
				uniqueFiles[relPath] = existingArr
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Found %d references across %d files:\n\n", len(refData), len(uniqueFiles)))
			for key, lines := range uniqueFiles {
				sb.WriteString(fmt.Sprintf("%s:\n", key))
				sort.Strings(lines)
				for _, line := range lines {
					sb.WriteString(line)
				}
			}

			// Convert back to 1-based
			return sb.String()

		}
		return "LSP is not implemented yet"
	default:
		cliColors.StdErr.Fprintf(os.Stderr, "Tool called but cannot be handled: %s\n", call.Function.Name)
		return fmt.Sprintf("Attempted a tool call on %s but there is no tool of that name.", call.Function.Name)
	}
}

var skillArgPattern = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]|\$(\d+)`)

// buildSkillMessages turns a prompt into the user messages to send to the LLM.
// A prompt starting with / invokes one or more skills, e.g. "/apple /grape a b".
// All leading /name tokens are skills and every remaining token is shared
// across each skill's argument substitution.
func buildSkillMessages(prompt string, skills map[string]*Document) ([]openai.ChatCompletionMessageParamUnion, error) {
	if !strings.HasPrefix(prompt, "/") {
		return []openai.ChatCompletionMessageParamUnion{userMessage(prompt)}, nil
	}

	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return nil, fmt.Errorf("prompt must name at least one skill")
	}

	skillNames := make([]string, 0, len(fields))
	i := 0
	for i < len(fields) && strings.HasPrefix(fields[i], "/") {
		name := strings.ToLower(strings.TrimPrefix(fields[i], "/"))
		if name == "" {
			return nil, fmt.Errorf("invalid skill name %q", fields[i])
		}
		skillNames = append(skillNames, name)
		i++
	}

	args := fields[i:]

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(skillNames))
	for _, name := range skillNames {
		skill, ok := skills[name]
		if !ok {
			return nil, fmt.Errorf("unknown skill: %q", name)
		}
		_, err := os.Stat(fmt.Sprintf(".claude/skills/%s/scripts", name))
		if err == nil {
			skillMsg := fmt.Sprintf(
				"Skill: %s (located at .claude/skills/%s)\nPaths in the instructions below are relative to that folder.\n\n%s",
				name,
				name,
				substituteArgs(skill.Body, args),
			)
			messages = append(messages, userMessage(skillMsg))
		} else {
			messages = append(messages, userMessage(substituteArgs(skill.Body, args)))
		}

	}

	return messages, nil
}

// substituteArgs replaces $ARGUMENTS, $ARGUMENTS[n] and $n in a skill body.
func substituteArgs(body string, args []string) string {
	replacedBody := skillArgPattern.ReplaceAllStringFunc(body, func(match string) string {
		var index int

		if strings.HasPrefix(match, "$ARGUMENTS[") {
			fmt.Sscanf(match, "$ARGUMENTS[%d]", &index)
		} else {
			fmt.Sscanf(match, "$%d", &index)
		}

		if index >= 0 && index < len(args) {
			return args[index]
		}

		return ""
	})

	// Has to be done after numeric args replaced
	return strings.ReplaceAll(replacedBody, "$ARGUMENTS", strings.Join(args, " "))
}

func userMessage(content string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Content: openai.ChatCompletionUserMessageParamContentUnion{
				OfString: openai.String(content),
			},
		},
	}
}

func toolMessage(content string, toolCallID string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfTool: &openai.ChatCompletionToolMessageParam{
			Content: openai.ChatCompletionToolMessageParamContentUnion{
				OfString: openai.String(content),
			},
			ToolCallID: toolCallID,
		},
	}
}
