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

// ReviewKind selects which built-in doctrine an empty template field falls back
// to: a focused review of the pending diff, or a full sweep of the whole
// codebase that continues until nothing remains to improve.
type ReviewKind string

const (
	KindDiff ReviewKind = "diff" // review the current uncommitted changes
	KindFull ReviewKind = "full" // audit the entire codebase until nothing remains
)

// normKind defaults blank/unknown to the diff review.
func normKind(s string) ReviewKind {
	if ReviewKind(s) == KindFull {
		return KindFull
	}
	return KindDiff
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
//
// Authoring notes:
//   - Templates are written multi-line for readability; Render() flattens them.
//   - The mandatory verdict instruction ({{.Verdict}}, plus {{.AskBlock}} when
//     asking) is auto-appended by Render even if a template omits it, so custom
//     prompts can't accidentally break convergence. Keeping the tokens here too
//     is harmless (Render de-dupes by only appending when missing).
//   - Rules below are distilled from the project's go-audit doctrine: zero-trust,
//     verify-don't-guess, fix root causes (not just the diff), run the gates,
//     keep edits uncommitted so the other reviewer can see them.
const (
	enRules = `Work like a zero-trust third-party auditor: ` +
		`(1) Read the actual code/docs before judging — do not guess APIs or behavior; verify. ` +
		`(2) Look beyond the diff: if a real bug elsewhere is exposed or related, fix it too. ` +
		`(3) Cover correctness, error handling, concurrency/races, edge cases (nil, bounds, overflow), resource cleanup, and API misuse. ` +
		`(4) Fix the root cause with complete, atomic edits — no TODOs, no placeholders, no fake simplification, no undoing the other reviewer's correct changes. ` +
		`(5) After editing, run the project's gates (build, vet, tests, formatter) and make sure they pass. ` +
		`(6) Do NOT commit or stage — leave your changes uncommitted in the work tree so the other reviewer can see them via git diff. `

	enIntro = `You are one of two AI code reviewers taking turns on this repository. ` +
		`You and the other agent alternate: each turn re-reviews ALL the current changes (the whole git diff, no matter who made which edit) and fixes any remaining problems, ` +
		`and the loop continues until neither of you can find anything left to fix. `

	enCodexFirst = enIntro + enRules +
		`Start by running git diff (and git status) to see the current uncommitted changes, then review every one of them. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enCodexNext = `The other agent just took a turn ({{.Handoff}}). ` + enRules +
		`Re-review ALL the current changes (git diff) — the entire set, regardless of who made which edit — and fix anything still wrong. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enClaudeFirst = enIntro + enRules +
		`Start by running git diff (and git status) to see the current uncommitted changes, then review every one of them. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enClaudeNext = `The other agent just took a turn ({{.Handoff}}). ` + enRules +
		`Re-review ALL the current changes (git diff) — the entire set, regardless of who made which edit — and fix anything still wrong. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`

	zhRules = `请以零信任的第三方审查员视角工作：` +
		`(1) 下结论前先读真实代码/文档，不要臆断 API 或行为，要查证；` +
		`(2) 不要只盯着 diff——如果发现相关或被牵连的真实 bug，一并修复；` +
		`(3) 覆盖正确性、错误处理、并发/竞态、边界情况（nil、越界、溢出）、资源释放、API 误用；` +
		`(4) 修根因，改动要完整、原子——不留 TODO、不留占位、不做虚假简化、不撤销对方正确的改动；` +
		`(5) 改完后运行项目的门禁（构建、vet、测试、格式化）并确保通过；` +
		`(6) 不要提交或暂存——把改动留在工作区未提交，好让另一个审查员通过 git diff 看到。`

	zhIntro = `你是两个 AI 代码审查员之一，正在轮流审查本仓库。` +
		`你和另一个 agent 交替进行：每一轮都重新审查当前【全部】改动（整个 git diff，不管是谁改的）并修复仍存在的问题，循环直到双方都再也挑不出任何需要修改的地方为止。`

	zhCodexFirst = zhIntro + zhRules +
		`先运行 git diff（和 git status）查看当前未提交的全部改动，然后逐一审查。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhCodexNext = `另一个 agent 刚审查了一轮（{{.Handoff}}）。` + zhRules +
		`重新审查当前【全部】改动（git diff）——整套改动，不管是谁改的——并修复仍有问题的地方。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhClaudeFirst = zhIntro + zhRules +
		`先运行 git diff（和 git status）查看当前未提交的全部改动，然后逐一审查。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhClaudeNext = `另一个 agent 刚审查了一轮（{{.Handoff}}）。` + zhRules +
		`重新审查当前【全部】改动（git diff）——整套改动，不管是谁改的——并修复仍有问题的地方。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
)

// Full-review (whole-codebase sweep) built-in templates. Unlike the diff review,
// these direct the two agents to systematically audit the ENTIRE repository over
// successive turns — not just the pending changes — and keep going until both
// sides agree nothing is left to improve. codex and claude share the same text.
const (
	enFullIntro = `You are one of two AI code reviewers performing a FULL audit of this entire repository, taking turns. ` +
		`You and the other agent alternate sweeping the whole codebase — not just recent changes — each fixing real bugs the other may have missed, ` +
		`and the loop continues until you both agree the entire codebase is clean with nothing left to improve. `

	enFullRules = `Work like a zero-trust third-party auditor over the whole project: ` +
		`(1) Read the actual code/docs before judging — do not guess APIs or behavior; verify. ` +
		`(2) Sweep systematically: survey the source tree, and each turn pick the riskiest area not yet audited and read it in full — cover the entire codebase across the rounds, not a single file. ` +
		`(3) Cover correctness, error handling, concurrency/races, edge cases (nil, bounds, overflow), resource cleanup, API misuse, and clear performance or maintainability defects. ` +
		`(4) Fix the root cause with complete, atomic edits — no TODOs, no placeholders, no fake simplification, no undoing the other reviewer's correct changes. ` +
		`(5) After editing, run the project's gates (build, vet, tests, formatter) and make sure they pass. ` +
		`(6) Do NOT commit or stage — leave your changes uncommitted in the work tree so the other reviewer can see them via git diff. `

	enFullFirst = enFullIntro + enFullRules +
		`Begin the sweep now: survey the repository layout, then deep-read and audit the area you judge riskiest, fixing what you find. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	enFullNext = `The other agent just took an audit turn ({{.Handoff}}). ` + enFullRules +
		`Keep auditing the whole project: review the current changes (git diff) AND continue through code not yet covered, fixing anything still wrong. Stop only when nothing is left to fix. ` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`

	zhFullIntro = `你是两个 AI 代码审查员之一，正在对整个仓库做全量审查，轮流进行。` +
		`你和另一个 agent 交替遍历整个代码库（不只是最近的改动），各自修复对方可能遗漏的真实 bug，循环直到双方都认为整个代码库已经干净、没有任何可改进之处。`

	zhFullRules = `请以零信任的第三方审查员视角，对整个项目工作：` +
		`(1) 下结论前先读真实代码/文档，不要臆断 API 或行为，要查证；` +
		`(2) 系统性地遍历：先了解源码树结构，每一轮挑选尚未审查、风险最高的区域并完整读完——在多轮中覆盖整个代码库，而不是只看一个文件；` +
		`(3) 覆盖正确性、错误处理、并发/竞态、边界情况（nil、越界、溢出）、资源释放、API 误用，以及明显的性能或可维护性缺陷；` +
		`(4) 修根因，改动要完整、原子——不留 TODO、不留占位、不做虚假简化、不撤销对方正确的改动；` +
		`(5) 改完后运行项目的门禁（构建、vet、测试、格式化）并确保通过；` +
		`(6) 不要提交或暂存——把改动留在工作区未提交，好让另一个审查员通过 git diff 看到。`

	zhFullFirst = zhFullIntro + zhFullRules +
		`现在开始遍历：先了解仓库结构，然后深入阅读并审查你判断风险最高的区域，发现问题就修复。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
	zhFullNext = `另一个 agent 刚审查了一轮（{{.Handoff}}）。` + zhFullRules +
		`继续审查整个项目：既要审查当前已有的改动（git diff），也要继续遍历尚未覆盖的代码，修复仍有问题的地方。直到再也没有可修改的地方才停止。` +
		`{{.ReplyLang}} {{.Verdict}}{{if .Ask}} {{.AskBlock}}{{end}}`
)

// DefaultPrompts is the exported view of the built-in (first, next) template for
// a kind+side+language. The web UI shows these as placeholders so an empty
// per-agent prompt is understood as "use this default", and the user can
// copy/edit it.
func DefaultPrompts(kind, side, lang string) (first, next string) {
	return defaultPrompts(normKind(kind), side, normLang(lang))
}

// defaultPrompts returns the (first, next) default template for a
// kind+side+language. The full-review kind shares one text across both sides.
func defaultPrompts(kind ReviewKind, side string, l Lang) (first, next string) {
	if kind == KindFull {
		if l == LangZH {
			return zhFullFirst, zhFullNext
		}
		return enFullFirst, enFullNext
	}
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
func NewPromptSet(kind ReviewKind, side, first, next, lang string, askPrompt ...string) (*PromptSet, error) {
	l := normLang(lang)
	var ask string
	if len(askPrompt) > 0 {
		ask = askPrompt[0]
	}
	defFirst, defNext := defaultPrompts(kind, side, l)
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
	out := flatten(buf.String())

	// Force the machine-parseable verdict onto the end even if a custom template
	// forgot it — without it the bridge can never detect convergence. We append
	// only what's missing so well-formed templates aren't duplicated.
	if !strings.Contains(out, "AUDIT_RESULT") {
		out = flatten(out + " " + data.Verdict)
	}
	if ask && !strings.Contains(out, "NO_MORE_BUGS") {
		out = flatten(out + " " + data.AskBlock)
	}
	return out
}

// flatten collapses all whitespace runs (including newlines) into single spaces
// so the result is exactly one line, safe to submit via send-keys.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
