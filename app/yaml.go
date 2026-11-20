package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"
	"go.yaml.in/yaml/v4"
)

type Document struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Body        string
	Context     string `yaml:"context"`
}

// AI coding the yaml parser
func ParseDocument(data []byte) (*Document, error) {
	// Normalize line endings.
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	// Find the opening frontmatter delimiter.
	if !strings.HasPrefix(content, "---\n") {
		return nil, fmt.Errorf("missing opening frontmatter delimiter")
	}

	content = strings.TrimPrefix(content, "---\n")

	// Find the closing frontmatter delimiter.
	separator := "\n---"
	idx := strings.Index(content, separator)
	if idx == -1 {
		return nil, fmt.Errorf("missing closing frontmatter delimiter")
	}

	frontmatter := content[:idx]
	body := strings.TrimSpace(content[idx+len(separator):])

	// Parse YAML frontmatter.
	var doc Document
	if err := yaml.NewDecoder(bytes.NewReader([]byte(frontmatter))).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	doc.Body = body

	return &doc, nil
}

func get_skills() (map[string]*Document, error) {
	entries, err := os.ReadDir(".claude/skills/")
	if err != nil {
		return nil, fmt.Errorf("failed to read skills directory: %w", err)
	}

	skills := make(map[string]*Document)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		path := fmt.Sprintf(".claude/skills/%s/SKILL.md", e.Name())

		dat, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read SKILL.md for %s: %w", e.Name(), err)
		}

		doc, err := ParseDocument(dat)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SKILL.md for %s: %w", e.Name(), err)
		}

		key := strings.ToLower(strings.TrimSpace(doc.Name))
		if key == "" {
			return nil, fmt.Errorf("skill at %s has an empty name", path)
		}
		if _, exists := skills[key]; exists {
			return nil, fmt.Errorf("duplicate skill name %q at %s", doc.Name, path)
		}

		skills[key] = doc
	}

	return skills, nil
}

func skills_to_system_prompt(skills map[string]*Document) openai.ChatCompletionMessageParamUnion {
	var sb strings.Builder

	sb.WriteString("You have access to the following skills:\n\n")

	keys := make([]string, 0, len(skills))
	for key := range skills {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		skill := skills[key]
		fmt.Fprintf(&sb, "- %s: %s\n", skill.Name, skill.Description)
	}

	sb.WriteString("\nIf a skill matches the user's request, call the Skill tool with its name and follow the instructions it returns.")

	return openai.ChatCompletionMessageParamUnion{
		OfSystem: &openai.ChatCompletionSystemMessageParam{
			Content: openai.ChatCompletionSystemMessageParamContentUnion{
				OfString: openai.String(sb.String()),
			},
		},
	}
}
