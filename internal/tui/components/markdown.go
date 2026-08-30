package components

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/yuin/goldmark"
	goldmarkast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	goldmarkparser "github.com/yuin/goldmark/parser"
	goldmarktext "github.com/yuin/goldmark/text"
)

// MarkdownStyles keeps model-authored Markdown on the same terminal-adaptive
// semantic palette as the surrounding transcript. Links remain inert text.
type MarkdownStyles struct {
	Text          lipgloss.Style
	Heading       lipgloss.Style
	Strong        lipgloss.Style
	Emphasis      lipgloss.Style
	Strikethrough lipgloss.Style
	Code          lipgloss.Style
	Quote         lipgloss.Style
	ListMarker    lipgloss.Style
	Link          lipgloss.Style
	TableHeader   lipgloss.Style
	TableBorder   lipgloss.Style
}

var terminalMarkdownParsers = sync.Pool{New: func() any {
	return goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()
}}

var (
	inlineTableDelimiterBoundary = regexp.MustCompile(`\|[ \t]+\|[ \t]*:?-{3,}:?`)
	inlineTableRowBoundary       = regexp.MustCompile(`\|[ \t]+\|`)
	tableDelimiterCell           = regexp.MustCompile(`^:?-{3,}:?$`)
)

type terminalMarkdownRenderer struct {
	source []byte
	width  int
	styles MarkdownStyles
}

func renderTerminalMarkdown(markdown string, width int, styles MarkdownStyles) string {
	if strings.TrimSpace(markdown) == "" {
		return ""
	}
	source := []byte(normalizeInlineMarkdownTables(markdown))
	parser := terminalMarkdownParsers.Get().(goldmarkparser.Parser)
	document := parser.Parse(goldmarktext.NewReader(source))
	terminalMarkdownParsers.Put(parser)
	renderer := terminalMarkdownRenderer{
		source: source,
		width:  max(8, width),
		styles: styles,
	}
	return strings.Trim(renderer.renderBlocks(document, renderer.width, "\n\n"), "\n")
}

func (renderer terminalMarkdownRenderer) renderBlocks(parent goldmarkast.Node, width int, separator string) string {
	blocks := make([]string, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if rendered := strings.TrimRight(renderer.renderBlock(child, width), "\n"); rendered != "" {
			blocks = append(blocks, rendered)
		}
	}
	return strings.Join(blocks, separator)
}

func (renderer terminalMarkdownRenderer) renderBlock(node goldmarkast.Node, width int) string {
	width = max(4, width)
	switch current := node.(type) {
	case *goldmarkast.Paragraph, *goldmarkast.TextBlock:
		return renderer.wrap(renderer.styles.Text.Render(renderer.renderInlineChildren(node)), width)
	case *goldmarkast.Heading:
		return renderer.wrap(renderer.styles.Heading.Render(renderer.renderInlineChildren(current)), width)
	case *goldmarkast.ThematicBreak:
		return renderer.styles.TableBorder.Render(strings.Repeat("─", width))
	case *goldmarkast.CodeBlock:
		return renderer.renderCode(current.Lines().Value(renderer.source), width)
	case *goldmarkast.FencedCodeBlock:
		return renderer.renderCode(current.Lines().Value(renderer.source), width)
	case *goldmarkast.Blockquote:
		content := renderer.renderBlocks(current, max(4, width-2), "\n\n")
		return prefixLines(content, renderer.styles.Quote.Render("│ "), "  ")
	case *goldmarkast.List:
		return renderer.renderList(current, width)
	case *extensionast.Table:
		return renderer.renderTable(current, width)
	case *goldmarkast.HTMLBlock:
		content := append([]byte(nil), current.Lines().Value(renderer.source)...)
		if current.HasClosure() {
			content = append(content, current.ClosureLine.Value(renderer.source)...)
		}
		return renderer.wrap(renderer.styles.Code.Render(string(content)), width)
	default:
		if node.FirstChild() != nil {
			return renderer.renderBlocks(node, width, "\n")
		}
		return ""
	}
}

func (renderer terminalMarkdownRenderer) renderInlineChildren(parent goldmarkast.Node) string {
	var builder strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		builder.WriteString(renderer.renderInline(child))
	}
	return builder.String()
}

func (renderer terminalMarkdownRenderer) renderInline(node goldmarkast.Node) string {
	switch current := node.(type) {
	case *goldmarkast.Text:
		value := string(current.Value(renderer.source))
		switch {
		case current.HardLineBreak():
			value += "\n"
		case current.SoftLineBreak():
			value += " "
		}
		return value
	case *goldmarkast.String:
		return string(current.Value)
	case *goldmarkast.CodeSpan:
		return renderer.styles.Code.Render(renderer.plainInlineChildren(current))
	case *goldmarkast.Emphasis:
		style := renderer.styles.Emphasis
		if current.Level == 2 {
			style = renderer.styles.Strong
		}
		return style.Render(renderer.renderInlineChildren(current))
	case *goldmarkast.Link:
		label := renderer.renderInlineChildren(current)
		destination := string(current.Destination)
		if label == "" {
			label = destination
		}
		if destination != "" && destination != label {
			label += " (" + destination + ")"
		}
		return renderer.styles.Link.Render(label)
	case *goldmarkast.AutoLink:
		return renderer.styles.Link.Render(string(current.Label(renderer.source)))
	case *goldmarkast.Image:
		label := renderer.plainInlineChildren(current)
		if label == "" {
			label = "image"
		}
		return renderer.styles.Code.Render("[" + label + "]")
	case *goldmarkast.RawHTML:
		return renderer.styles.Code.Render(string(current.Segments.Value(renderer.source)))
	case *extensionast.Strikethrough:
		return renderer.styles.Strikethrough.Render(renderer.renderInlineChildren(current))
	case *extensionast.TaskCheckBox:
		if current.IsChecked {
			return "[x] "
		}
		return "[ ] "
	default:
		return renderer.renderInlineChildren(node)
	}
}

func (renderer terminalMarkdownRenderer) plainInlineChildren(parent goldmarkast.Node) string {
	var builder strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		switch current := child.(type) {
		case *goldmarkast.Text:
			builder.Write(current.Value(renderer.source))
		case *goldmarkast.String:
			builder.Write(current.Value)
		default:
			builder.WriteString(renderer.plainInlineChildren(child))
		}
	}
	return builder.String()
}

func (renderer terminalMarkdownRenderer) renderCode(source []byte, width int) string {
	content := strings.TrimRight(string(source), "\n")
	if content == "" {
		return ""
	}
	content = renderer.wrap(renderer.styles.Code.Render(content), max(2, width-2))
	return prefixLines(content, "  ", "  ")
}

func (renderer terminalMarkdownRenderer) renderList(list *goldmarkast.List, width int) string {
	items := make([]string, 0, list.ChildCount())
	position := list.Start
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		item, ok := child.(*goldmarkast.ListItem)
		if !ok {
			continue
		}
		marker := renderer.styles.ListMarker.Render("•")
		if list.IsOrdered() {
			marker = renderer.styles.ListMarker.Render(fmt.Sprintf("%d.", position))
			position++
		}
		indentWidth := lipgloss.Width(marker) + 1
		content := renderer.renderBlocks(item, max(4, width-indentWidth), "\n")
		if content == "" {
			continue
		}
		items = append(items, prefixLines(content, marker+" ", strings.Repeat(" ", indentWidth)))
	}
	return strings.Join(items, "\n")
}

func (renderer terminalMarkdownRenderer) renderTable(markdownTable *extensionast.Table, width int) string {
	var headers []string
	rows := make([][]string, 0, markdownTable.ChildCount())
	for child := markdownTable.FirstChild(); child != nil; child = child.NextSibling() {
		switch current := child.(type) {
		case *extensionast.TableHeader:
			headers = renderer.renderTableRow(current)
		case *extensionast.TableRow:
			rows = append(rows, renderer.renderTableRow(current))
		}
	}
	if len(headers) == 0 {
		return ""
	}
	if len(headers) > 1 && width < len(headers)*12 {
		return renderer.renderTableRecords(headers, rows, width)
	}

	columnWidths, ok := tableColumnWidths(headers, rows, width)
	if !ok {
		return renderer.renderTableRecords(headers, rows, width)
	}
	lines := renderer.renderAlignedTableRow(headers, columnWidths, markdownTable.Alignments, renderer.styles.TableHeader)
	lines = append(lines, renderer.renderTableSeparator(columnWidths, "━"))
	for rowIndex, row := range rows {
		lines = append(lines, renderer.renderAlignedTableRow(row, columnWidths, markdownTable.Alignments, renderer.styles.Text)...)
		if rowIndex+1 < len(rows) {
			lines = append(lines, renderer.renderTableSeparator(columnWidths, "─"))
		}
	}
	return strings.Join(lines, "\n")
}

const (
	tableCellPadding = 1
	tableColumnGap   = 2
	tableMinColumn   = 3
)

func tableColumnWidths(headers []string, rows [][]string, width int) ([]int, bool) {
	columns := len(headers)
	if columns == 0 {
		return nil, false
	}
	reserved := columns*tableCellPadding*2 + (columns-1)*tableColumnGap
	available := width - reserved
	if available < columns*tableMinColumn {
		return nil, false
	}
	natural := make([]int, columns)
	for column, header := range headers {
		natural[column] = max(tableMinColumn, maximumLineWidth(header))
	}
	for _, row := range rows {
		for column := range natural {
			if column < len(row) {
				natural[column] = max(natural[column], maximumLineWidth(row[column]))
			}
		}
	}
	total := 0
	for _, columnWidth := range natural {
		total += columnWidth
	}
	if total <= available {
		return natural, true
	}

	result := make([]int, columns)
	remaining := available
	active := make([]int, columns)
	for column := range active {
		active[column] = column
	}
	for len(active) > 0 {
		share := remaining / len(active)
		fixed := false
		next := make([]int, 0, len(active))
		for _, column := range active {
			if natural[column] <= share {
				result[column] = natural[column]
				remaining -= result[column]
				fixed = true
				continue
			}
			next = append(next, column)
		}
		active = next
		if fixed {
			continue
		}
		share = remaining / len(active)
		extra := remaining % len(active)
		for position, column := range active {
			result[column] = share
			if position < extra {
				result[column]++
			}
		}
		break
	}
	return result, true
}

func maximumLineWidth(value string) int {
	maximum := 0
	for _, line := range strings.Split(value, "\n") {
		maximum = max(maximum, lipgloss.Width(line))
	}
	return maximum
}

func (renderer terminalMarkdownRenderer) renderAlignedTableRow(
	values []string,
	widths []int,
	alignments []extensionast.Alignment,
	style lipgloss.Style,
) []string {
	cells := make([][]string, len(widths))
	height := 1
	for column, columnWidth := range widths {
		value := ""
		if column < len(values) {
			value = values[column]
		}
		wrapped := renderer.wrap(value, columnWidth)
		if wrapped == "" {
			cells[column] = []string{""}
		} else {
			cells[column] = strings.Split(wrapped, "\n")
		}
		height = max(height, len(cells[column]))
	}

	lines := make([]string, 0, height)
	for rowLine := 0; rowLine < height; rowLine++ {
		segments := make([]string, len(widths))
		for column, columnWidth := range widths {
			value := ""
			if rowLine < len(cells[column]) {
				value = cells[column][rowLine]
			}
			alignment := lipgloss.Left
			if column < len(alignments) {
				switch alignments[column] {
				case extensionast.AlignRight:
					alignment = lipgloss.Right
				case extensionast.AlignCenter:
					alignment = lipgloss.Center
				}
			}
			cell := style.Width(columnWidth).Align(alignment).Render(value)
			segments[column] = " " + cell + " "
		}
		lines = append(lines, strings.Join(segments, strings.Repeat(" ", tableColumnGap)))
	}
	return lines
}

func (renderer terminalMarkdownRenderer) renderTableSeparator(widths []int, character string) string {
	segments := make([]string, len(widths))
	for column, columnWidth := range widths {
		segments[column] = strings.Repeat(character, columnWidth+tableCellPadding*2)
	}
	return renderer.styles.TableBorder.Render(strings.Join(segments, strings.Repeat(" ", tableColumnGap)))
}

func (renderer terminalMarkdownRenderer) renderTableRow(row goldmarkast.Node) []string {
	values := make([]string, 0, row.ChildCount())
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		values = append(values, strings.TrimSpace(renderer.renderInlineChildren(cell)))
	}
	return values
}

func (renderer terminalMarkdownRenderer) renderTableRecords(headers []string, rows [][]string, width int) string {
	labelWidth := 0
	for _, header := range headers {
		labelWidth = max(labelWidth, lipgloss.Width(header))
	}
	inline := labelWidth+10 <= width
	records := make([]string, 0, len(rows))
	for _, row := range rows {
		fields := make([]string, 0, len(headers))
		for column, header := range headers {
			value := ""
			if column < len(row) {
				value = row[column]
			}
			label := renderer.styles.TableHeader.Render(header)
			if inline {
				valueWidth := max(4, width-labelWidth-2)
				value = renderer.wrap(value, valueWidth)
				fields = append(fields, prefixLines(value,
					lipgloss.NewStyle().Width(labelWidth).Render(label)+"  ",
					strings.Repeat(" ", labelWidth+2)))
				continue
			}
			fields = append(fields, label+"\n"+prefixLines(renderer.wrap(value, max(4, width-2)), "  ", "  "))
		}
		records = append(records, strings.Join(fields, "\n"))
	}
	separator := "\n" + renderer.styles.TableBorder.Render(strings.Repeat("─", width)) + "\n"
	return strings.Join(records, separator)
}

func (renderer terminalMarkdownRenderer) wrap(value string, width int) string {
	return strings.TrimSpace(lipgloss.Wrap(value, max(1, width), ""))
}

func prefixLines(value, firstPrefix, continuationPrefix string) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		if index == 0 {
			lines[index] = firstPrefix + lines[index]
			continue
		}
		lines[index] = continuationPrefix + lines[index]
	}
	return strings.Join(lines, "\n")
}

// normalizeInlineMarkdownTables repairs only the unambiguous compact form
// sometimes emitted inside a JSON string: a header, delimiter, and body rows
// separated by "| |" on one physical line. Stored answer Markdown is unchanged.
func normalizeInlineMarkdownTables(markdown string) string {
	lines := strings.Split(markdown, "\n")
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		repaired, ok := repairInlineMarkdownTable(line)
		if !ok {
			normalized = append(normalized, line)
			continue
		}
		normalized = append(normalized, repaired...)
	}
	return strings.Join(normalized, "\n")
}

func repairInlineMarkdownTable(line string) ([]string, bool) {
	if !inlineTableDelimiterBoundary.MatchString(line) {
		return nil, false
	}
	expanded := strings.Split(inlineTableRowBoundary.ReplaceAllString(line, "|\n|"), "\n")
	if len(expanded) < 3 {
		return nil, false
	}
	delimiterIndex := -1
	var columns int
	for index, candidate := range expanded {
		cells, ok := parsePipeRow(candidate)
		if !ok || len(cells) == 0 {
			continue
		}
		valid := true
		for _, cell := range cells {
			if !tableDelimiterCell.MatchString(strings.TrimSpace(cell)) {
				valid = false
				break
			}
		}
		if valid {
			delimiterIndex = index
			columns = len(cells)
			break
		}
	}
	if delimiterIndex != 1 || columns == 0 {
		return nil, false
	}
	prefix, header, ok := trailingPipeRow(expanded[0], columns)
	if !ok {
		return nil, false
	}
	body := make([]string, 0, len(expanded)-2)
	trailing := ""
	for index := delimiterIndex + 1; index < len(expanded); index++ {
		row, remainder, rowOK := leadingPipeRow(expanded[index], columns)
		if !rowOK || remainder != "" && index != len(expanded)-1 {
			return nil, false
		}
		body = append(body, row)
		trailing = remainder
	}
	if len(body) == 0 {
		return nil, false
	}
	result := make([]string, 0, len(body)+5)
	if prefix != "" {
		result = append(result, prefix, "")
	}
	result = append(result, header, strings.TrimSpace(expanded[delimiterIndex]))
	result = append(result, body...)
	if trailing != "" {
		result = append(result, "", trailing)
	}
	return result, true
}

func trailingPipeRow(line string, columns int) (string, string, bool) {
	positions := unescapedPipePositions(line)
	if len(positions) < columns+1 {
		return "", "", false
	}
	start := positions[len(positions)-columns-1]
	end := positions[len(positions)-1]
	row := strings.TrimSpace(line[start : end+1])
	cells, ok := parsePipeRow(row)
	if !ok || len(cells) != columns || strings.TrimSpace(line[end+1:]) != "" {
		return "", "", false
	}
	return strings.TrimSpace(line[:start]), row, true
}

func leadingPipeRow(line string, columns int) (string, string, bool) {
	line = strings.TrimSpace(line)
	positions := unescapedPipePositions(line)
	if len(positions) < columns+1 || positions[0] != 0 {
		return "", "", false
	}
	end := positions[columns]
	row := strings.TrimSpace(line[:end+1])
	cells, ok := parsePipeRow(row)
	if !ok || len(cells) != columns {
		return "", "", false
	}
	return row, strings.TrimSpace(line[end+1:]), true
}

func parsePipeRow(row string) ([]string, bool) {
	row = strings.TrimSpace(row)
	positions := unescapedPipePositions(row)
	if len(positions) < 2 || positions[0] != 0 || positions[len(positions)-1] != len(row)-1 {
		return nil, false
	}
	cells := make([]string, 0, len(positions)-1)
	for index := 0; index < len(positions)-1; index++ {
		cells = append(cells, row[positions[index]+1:positions[index+1]])
	}
	return cells, true
}

func unescapedPipePositions(value string) []int {
	positions := make([]int, 0, strings.Count(value, "|"))
	for index := 0; index < len(value); index++ {
		if value[index] != '|' {
			continue
		}
		backslashes := 0
		for previous := index - 1; previous >= 0 && value[previous] == '\\'; previous-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			positions = append(positions, index)
		}
	}
	return positions
}
