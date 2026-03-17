package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// --- SPL AST Structures ---

type SPLProgram struct {
	Vaults     []SPLVault
	MainPrompt SPLPrompt
}

type SPLVault struct {
	Name   string
	Prompt SPLPrompt
}

type SPLPrompt struct {
	Name       string
	System     string
	LLM        string
	Model      string
	MinTier    string
	OnGrid     bool
	MaxTokens  int
}

// --- Lexer ---

type tokenType int

const (
	tokError tokenType = iota
	tokEOF
	tokIdentifier
	tokString
	tokKeyword
	tokParenOpen
	tokParenClose
	tokComma
	tokSemicolon
	tokAs
)

type token struct {
	typ   tokenType
	value string
}

type lexer struct {
	input  string
	pos    int
	tokens []token
}

func (l *lexer) tokenize() {
	for l.pos < len(l.input) {
		r := l.input[l.pos]
		if unicode.IsSpace(rune(r)) {
			l.pos++
			continue
		}
		if r == '-' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '-' {
			// Skip comment
			for l.pos < len(l.input) && l.input[l.pos] != '\n' {
				l.pos++
			}
			continue
		}

		switch r {
		case '(':
			l.tokens = append(l.tokens, token{tokParenOpen, "("})
			l.pos++
		case ')':
			l.tokens = append(l.tokens, token{tokParenClose, ")"})
			l.pos++
		case ',':
			l.tokens = append(l.tokens, token{tokComma, ","})
			l.pos++
		case ';':
			l.tokens = append(l.tokens, token{tokSemicolon, ";"})
			l.pos++
		case '\'', '"':
			l.tokens = append(l.tokens, l.lexString(r))
		default:
			if unicode.IsLetter(rune(r)) {
				l.tokens = append(l.tokens, l.lexIdentifier())
			} else {
				l.pos++ // skip unknown
			}
		}
	}
	l.tokens = append(l.tokens, token{tokEOF, ""})
}

func (l *lexer) lexString(quote byte) token {
	l.pos++ // skip open quote
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != quote {
		l.pos++
	}
	val := l.input[start:l.pos]
	if l.pos < len(l.input) {
		l.pos++ // skip close quote
	}
	return token{tokString, val}
}

func (l *lexer) lexIdentifier() token {
	start := l.pos
	for l.pos < len(l.input) && (unicode.IsLetter(rune(l.input[l.pos])) || unicode.IsDigit(rune(l.input[l.pos])) || l.input[l.pos] == '_') {
		l.pos++
	}
	val := l.input[start:l.pos]
	up := strings.ToUpper(val)
	if up == "AS" {
		return token{tokAs, val}
	}
	// Check if it's a known keyword
	keywords := []string{"WITH", "PROMPT", "SELECT", "GENERATE", "USING", "MODEL", "ON", "GRID", "SYSTEM_ROLE", "LLM"}
	for _, k := range keywords {
		if up == k {
			return token{tokKeyword, up}
		}
	}
	return token{tokIdentifier, val}
}

// --- Parser ---

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token {
	return p.tokens[p.pos]
}

func (p *parser) consume() token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

func (p *parser) expect(typ tokenType) token {
	t := p.consume()
	if t.typ != typ {
		// In a real parser we'd return an error, for now we panic or handle loosely
	}
	return t
}

func (p *parser) parse() SPLProgram {
	prog := SPLProgram{}
	if p.peek().typ == tokKeyword && p.peek().value == "WITH" {
		p.consume() // WITH
		for {
			vName := p.expect(tokIdentifier).value
			p.expect(tokAs)
			p.expect(tokParenOpen)
			vPrompt := p.parsePrompt()
			p.expect(tokParenClose)
			prog.Vaults = append(prog.Vaults, SPLVault{vName, vPrompt})
			if p.peek().typ == tokComma {
				p.consume()
				continue
			}
			break
		}
	}
	prog.MainPrompt = p.parsePrompt()
	if p.peek().typ == tokSemicolon {
		p.consume()
	}
	return prog
}

func (p *parser) parsePrompt() SPLPrompt {
	pr := SPLPrompt{MaxTokens: 1024}
	for p.pos < len(p.tokens) {
		t := p.peek()
		if t.typ == tokEOF || t.typ == tokParenClose || t.typ == tokSemicolon {
			break
		}
		if t.typ == tokKeyword {
			switch t.value {
			case "PROMPT":
				p.consume()
				pr.Name = p.expect(tokIdentifier).value
			case "SELECT":
				p.consume()
				// Handle functions like system_role('...')
				for p.peek().typ == tokIdentifier || p.peek().typ == tokKeyword {
					fname := strings.ToUpper(p.consume().value)
					p.expect(tokParenOpen)
					val := p.expect(tokString).value
					p.expect(tokParenClose)
					if fname == "SYSTEM_ROLE" || fname == "SYSTEM" {
						pr.System = val
					}
					if p.peek().typ == tokComma {
						p.consume()
						continue
					}
					break
				}
			case "GENERATE":
				p.consume()
				// Handle llm('...')
				if p.peek().typ == tokIdentifier || p.peek().typ == tokKeyword {
					p.consume() // llm or similar
					p.expect(tokParenOpen)
					pr.LLM = p.expect(tokString).value
					p.expect(tokParenClose)
				}
			case "USING":
				p.consume()
				p.expect(tokKeyword) // MODEL
				pr.Model = p.expect(tokString).value
			case "ON":
				p.consume()
				p.expect(tokKeyword) // GRID
				pr.OnGrid = true
			default:
				p.consume()
			}
		} else {
			p.consume()
		}
	}
	return pr
}

// --- Executor ---

func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	hubURL := fs.String("hub-url", "", "Hub URL")
	fs.Parse(args)

	if fs.NArg() == 0 {
		return fmt.Errorf("usage: mg run <file.spl>")
	}
	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	url := ResolveHubURL(*hubURL)

	l := lexer{input: string(data)}
	l.tokenize()
	p := parser{tokens: l.tokens}
	prog := p.parse()

	// Execute Vaults in parallel
	vaultResults := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, v := range prog.Vaults {
		wg.Add(1)
		go func(v SPLVault) {
			defer wg.Done()
			res, err := executeSPLTask(url, v.Prompt)
			if err == nil {
				mu.Lock()
				vaultResults[v.Name] = res
				mu.Unlock()
			}
		}(v)
	}
	wg.Wait()

	// Replace variables in main prompt
	finalPrompt := prog.MainPrompt
	for name, res := range vaultResults {
		finalPrompt.LLM = strings.ReplaceAll(finalPrompt.LLM, "{"+name+"}", res)
	}

	_, err = executeSPLTask(url, finalPrompt)
	return err
}

func executeSPLTask(hubURL string, pr SPLPrompt) (string, error) {
	taskID := uuid.New().String()
	fmt.Printf("\n--- [%s] ---\n", pr.Name)

	payload := map[string]interface{}{
		"task_id":    taskID,
		"model":      pr.Model,
		"prompt":     pr.LLM,
		"system":     pr.System,
		"max_tokens": pr.MaxTokens,
	}

	body, _ := json.Marshal(payload)
	resp, err := http.Post(fmt.Sprintf("%s/tasks", hubURL), "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(5 * time.Minute)
	interval := 2 * time.Second
	for time.Now().Before(deadline) {
		getResp, err := http.Get(fmt.Sprintf("%s/tasks/%s", hubURL, taskID))
		if err != nil {
			time.Sleep(interval)
			continue
		}

		var task map[string]interface{}
		json.NewDecoder(getResp.Body).Decode(&task)
		getResp.Body.Close()

		state, _ := task["state"].(string)
		if state == "COMPLETE" {
			res, _ := task["result"].(map[string]interface{})
			if res == nil {
				res = task
			}
			content := str(res, "content")
			fmt.Printf("%s\n", content)
			agentInfo := str(res, "agent_name")
			if agentInfo == "" {
				agentInfo = str(res, "agent_host")
			}
			if agentInfo == "" {
				agentInfo = str(res, "agent_id")
			}
			completedAt := str(res, "completed_at")
			fmt.Printf("\n[model=%s tokens=%.0f+%.0f latency=%.0fms agent=%s completed=%s]\n",
				str(res, "model"), num(res, "input_tokens"), num(res, "output_tokens"), num(res, "latency_ms"),
				agentInfo, completedAt)
			return content, nil
		}
		if state == "FAILED" {
			return "", fmt.Errorf("task failed")
		}
		time.Sleep(interval)
	}
	return "", fmt.Errorf("timeout")
}
