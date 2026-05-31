package bridge

import (
	"bytes"
	"strings"
	"text/template"
)

// Prompts are user-configurable Go text/templates and come in built-in English
// and Chinese variants. The two agents communicate only through the shared git
// work tree plus a short handoff note; the templates shape what each agent is
// told to do each turn and which language to reply in.
//
// CRITICAL: a rendered prompt MUST end up single-line. It is submitted to an
// interactive TUI where an embedded newline is a literal Enter that submits early.
// Render() flattens whitespace so multi-line templates are safe to author.
//
// The machine tokens (AUDIT_RESULT, CLEAN/FIXED/ISSUES, NO_MORE_BUGS/MORE_BUGS)
// are NEVER translated — the bridge parses them. Only the surrounding human
// instructions and the "reply in language X" directive change with language.

// Lang identifies a built-in prompt language.
type Lang string

const (
	LangEN Lang = "en"
	LangZH Lang = "zh"
)

// normLang defaults blank/unknown to English.
func normLang(s string) Lang {
	if s == string(LangZH) {
		return LangZH
	}
	return LangEN
}

// verdictInstruction returns the language-appropriate instruction for ending the
// reply with a parseable verdict line. The token stays in ASCII.
func verdictInstruction(l Lang) string {
	if l == LangZH {
		return "回复的最后一行只写：token AUDIT_RESULT 加冒号再加一个英文单词——" +
			"没有发现问题且未改动代码写 CLEAN，发现问题并已修改代码写 FIXED，发现问题但未修改写 ISSUES。"
	}
	return "Finish with a final line: the token AUDIT_RESULT then a colon and one word — " +
		"CLEAN if you found no problems and changed nothing, FIXED if you edited the code to fix problems, " +
		"or ISSUES if you found problems but did not fix them."
}

// askInstruction returns the language-appropriate ask-gate confirmation request.
func askInstruction(l Lang, custom string) string {
	custom = strings.TrimSpace(custom)
	if l == LangZH {
		if custom != "" {
			return custom + " 并且在 AUDIT_RESULT 的下一行：如果你确信代码已没有任何遗留 bug，写 token NO_MORE_BUGS；" +
				"如果还有任何需要处理的地方，写 token MORE_BUGS。"
		}
		return "并且在 AUDIT_RESULT 的下一行：如果你确信代码已没有任何遗留 bug，写 token NO_MORE_BUGS；" +
			"如果还有任何需要处理的地方，写 token MORE_BUGS。"
	}
	if custom != "" {
		return custom + " On the line after AUDIT_RESULT, output the token NO_MORE_BUGS if you are confident " +
			"there are no remaining bugs of any kind, or MORE_BUGS if anything still needs attention."
	}
	return "Also, on the line after AUDIT_RESULT, output the token NO_MORE_BUGS if you are confident " +
		"there are no remaining bugs of any kind, or MORE_BUGS if anything still needs attention."
}

// replyLangDirective tells the agent which language to write its prose in.
func replyLangDirective(l Lang) string {
	if l == LangZH {
		return "请用中文撰写你的分析和报告。"
	}
	return "Write your analysis and report in English."
}

// Built-in default templates per language. Empty per-agent config falls back to
// the matching language's default.
const (
	enIntro = `You are one of two AI code reviewers taking turns on this repository. ` +
		`You and the other agent alternate: each reviews the current uncommitted changes, ` +
		`fixes bugs the other may have missed, and the loop continues until you both agree the code is clean. `

	enCodexFirst = enIntro +
		`Review all current uncommitted changes in this repository (run git diff). ` +
		`Check for bugs, logic errors, edge cases, and type-safety problems. ` +
		`If you are confident about a fix, edit the code directly; do not refactor unrelated code. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enCodexNext = `The other agent just reviewed the changes ({{.Handoff}}). ` +
		`Re-review the current uncommitted changes (git diff) and fix anything you are confident is a real bug; ` +
		`do not undo their correct changes. {{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enClaudeFirst = enIntro +
		`Review all current uncommitted changes in this repository (run git diff). ` +
		`Look for bugs, logic errors, edge cases, and type-safety problems, and fix anything you are confident about. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enClaudeNext = `Codex just reviewed (and possibly edited) the current uncommitted changes ({{.Handoff}}). ` +
		`Audit the current state (git diff): verify the edits are correct and look for remaining bugs. ` +
		`Fix anything you are confident is a real bug; do not undo correct changes. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`

	zhIntro = `你是两个 AI 代码审查员之一，正在轮流审查本仓库。` +
		`你和另一个 agent 交替进行：每人审查当前未提交的改动、修复对方可能遗漏的 bug，循环直到双方都认为代码干净。`

	zhCodexFirst = zhIntro +
		`审查本仓库当前所有未提交的改动（运行 git diff 查看）。` +
		`检查 bug、逻辑错误、边界情况和类型安全问题。` +
		`如果你有把握，直接修改代码；不要重构无关代码。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhCodexNext = `另一个 agent 刚审查了改动（{{.Handoff}}）。` +
		`重新审查当前未提交的改动（git diff），修复你确信是真 bug 的地方；不要撤销对方正确的改动。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhClaudeFirst = zhIntro +
		`审查本仓库当前所有未提交的改动（运行 git diff 查看）。` +
		`查找 bug、逻辑错误、边界情况和类型安全问题，并修复你有把握的地方。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhClaudeNext = `Codex 刚审查（并可能修改）了当前未提交的改动（{{.Handoff}}）。` +
		`审计当前状态（git diff）：核对它的修改是否正确，并查找遗留 bug。` +
		`修复你确信是真 bug 的地方；不要撤销正确的改动。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
)

// defaultPrompts returns the (first, next) default template for a side+language.
func defaultPrompts(side string, l Lang) (first, next string) {
	switch {
	case side == "codex" && l == LangZH:
		return zhCodexFirst, zhCodexNext
	case side == "codex":
		return enCodexFirst, enCodexNext
	case l == LangZH:
		return zhClaudeFirst, zhClaudeNext
	default:
		return enClaudeFirst, enClaudeNext
	}
}

// PromptSet is one side's configured (or default) templates, precompiled, bound
// to a language for the instruction blocks.
type PromptSet struct {
	first     *template.Template
	next      *template.Template
	lang      Lang
	askPrompt string
}

// NewPromptSet compiles a side's templates for the given language, falling back
// to that language's built-in defaults when a string is empty. Returns an error
// if a non-empty custom template is malformed (surfaced by the UI).
func NewPromptSet(side, first, next, lang string, askPrompt ...string) (*PromptSet, error) {
	l := normLang(lang)
	var ask string
	if len(askPrompt) > 0 {
		ask = askPrompt[0]
	}
	defFirst, defNext := defaultPrompts(side, l)
	if strings.TrimSpace(first) == "" {
		first = defFirst
	}
	if strings.TrimSpace(next) == "" {
		next = defNext
	}
	ft, err := template.New("first").Parse(first)
	if err != nil {
		return nil, err
	}
	nt, err := template.New("next").Parse(next)
	if err != nil {
		return nil, err
	}
	return &PromptSet{first: ft, next: nt, lang: l, askPrompt: ask}, nil
}

type promptData struct {
	Handoff   string
	Ask       bool
	Verdict   string
	AskBlock  string
	ReplyLang string
}

// Render builds the prompt for a turn. handoff=="" selects the first-turn
// template. The result is flattened to a single line.
func (p *PromptSet) Render(handoff string, ask bool) string {
	tmpl := p.next
	if strings.TrimSpace(handoff) == "" {
		tmpl = p.first
	}
	data := promptData{
		Handoff:   handoff,
		Ask:       ask,
		Verdict:   verdictInstruction(p.lang),
		AskBlock:  askInstruction(p.lang, p.askPrompt),
		ReplyLang: replyLangDirective(p.lang),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		// On a render error fall back to a minimal safe prompt rather than crash.
		return flatten("Review the current git diff for bugs and fix what you can. " + verdictInstruction(p.lang))
	}
	return flatten(buf.String())
}

// flatten collapses all whitespace runs (including newlines) into single spaces
// so the result is exactly one line, safe to submit via send-keys.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
