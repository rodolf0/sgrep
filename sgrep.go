package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
)

var nscopes = flag.Uint("n", 1, "Number of outer scopes to output")
var only = flag.Bool("only", false, "Don't print the surrounding line context")
var pretty = flag.Bool("pretty", false, "Use colors")
var pattern *regexp.Regexp
var delims map[string]*Delimiter

func init() {
	flag.Parse()
	pattern = regexp.MustCompile(flag.Arg(0))
	delims = map[string]*Delimiter{
		"(": {")", false}, ")": {"(", true},
		"[": {"]", false}, "]": {"[", true},
		"{": {"}", false}, "}": {"{", true},
		"/*": {"*/", false}, "*/": {"/*", true},
	}
}

type Delimiter struct {
	str  string
	open bool
}

type Line struct {
	line []byte
	num  uint
}

type Marker struct {
	delim *Delimiter
	line  *Line
	col   uint
}

type Markers []*Marker

func (m Markers) Len() int      { return len(m) }
func (m Markers) Swap(i, j int) { m[i], m[j] = m[j], m[i] }
func (m Markers) Less(i, j int) bool {
	return (m[i].line.num < m[j].line.num) ||
		(m[i].line.num == m[j].line.num && m[i].col < m[j].col)
}

func (l *Line) findMarkers() Markers {
	markers := make(Markers, 0, 4)
	for _, val := range delims {
		// find all instances of this marker
		for base := 0; base < len(l.line); {
			if idx := bytes.Index(l.line[base:], []byte(val.str)); idx != -1 {
				markers = append(markers,
					&Marker{delim: val, line: l, col: uint(idx + base)})
				base += idx + 1
			} else {
				break
			}
		}
	}
	sort.Sort(markers)
	return markers
}

type Scope struct {
	parent *Scope  // scope containing this one
	childs []*Scope
	start  *Marker
	end    *Marker
	match  bool  // scope contains a match, so it needs to be printed
}

func (s *Scope) String() string {
	if s.end != nil {
		return fmt.Sprintf("%v:%v - %v:%v",
			s.start.line.num, s.start.col,
			s.end.line.num, s.end.col)
	}
	return fmt.Sprintf("%v:%v - *", s.start.line.num, s.start.col)
}

func (s *Scope) contains(line, col0, col1 uint) bool {
	return (
		(s.start.line.num < line || (
			s.start.line.num == line && s.start.col <= col0)) &&
		(s.end == nil ||
			(s.end.line.num > line || (
				s.end.line.num == line && s.end.col >= col1))))
}

type Context struct {
	open   []*Scope // currently open scopes, last is tightest
	closed []*Scope // closed scopes, first is tightest, last is broadest
	buffer []*Line
}

func (c *Context) markNScopes(N, line, col0, col1 uint) {
	// look for the tightest scope containing this parameters
	var start *Scope = nil
	if len(c.closed) > 0 {
		// ASSERT c.closed is ordered from tightest to broadest
		for _, s := range c.closed {
			if s.contains(line, col0, col1) {
				start = s
				break
			}
		}
	}
	if start == nil && len(c.open) > 0 {
		// ASSERT c.open is ordered from broadest to thightest
		for i := len(c.open)-1; i >= 0; i-- {
			tightest := c.open[i]
			if tightest.contains(line, col0, col1) {
				start = tightest
				break
			}
		}
	}
	for n := uint(0); n < N && start != nil; n++ {
		//fmt.Printf("Marking %v\n", start)
		start.match = true
		start = start.parent
	}
}

func (c *Context) parseScopes(line *Line) {
	markers := line.findMarkers()
	for _, m := range markers {
		if m.delim.open {
			newscope := &Scope{parent: nil, childs: nil, start: m, end: nil, match: false}
			if len(c.open) > 0 {
				// last open scope will be parent of this new one
				parent := c.open[len(c.open)-1]
				parent.childs = append(parent.childs, newscope)
				newscope.parent = parent
			}
			c.open = append(c.open, newscope)
		} else {
			// if close doesn't match top of the stack, discard
			if len(c.open) == 0 {
				continue
			}
			// check if top of the stack is the opening marker for this closing
			top := c.open[len(c.open)-1]
			if opposite := delims[m.delim.str]; opposite != top.start.delim {
				continue
			}
			// pop the scope out of open, into closed list
			c.open = c.open[:len(c.open)-1]
			top.end = m
			c.closed = append(c.closed, top)
		}
	}
}

func (c *Context) flushMatching(out io.Writer, openScopes bool) {
	c.consolidateClosed()
	for _, s := range c.closed {
		if s.match {
			fmt.Println(s)
		}
	}
	c.closed = nil
	if openScopes {
		for _, s := range c.open {
			if s.match {
				fmt.Println(s)
			}
		}
	}
}

// discard closed scopes which didn't match
// if a scope and it's parent have a match, only keep parent
func (c *Context) consolidateClosed() {
	closed := make([]*Scope, 0, len(c.closed))
	moved := make(map[*Scope]struct{})
	for _, scope := range c.closed {
		if scope.match {
			// search for largest-containing-matching scope
			for scope.parent != nil && scope.parent.match {
					scope = scope.parent
			}
			// only insert once and if closed scope
			if _, ok := moved[scope]; !ok && scope.end != nil {
				closed = append(closed, scope)
				moved[scope] = struct{}{}
			}
		}
	}
	c.closed = closed
}

func main() {
	in := bufio.NewReader(os.Stdin)
	ctx := Context{open: nil, closed: nil, buffer: make([]*Line, 0, 16)}

	line_number := uint(0)
	for {
		if line, err := in.ReadSlice('\n'); err != nil {
			if err == io.EOF {
				break
			}
			panic(err)
		} else {
			line := &Line{line: line, num: line_number}
			ctx.parseScopes(line)
			// keep buffer of lines if there's an open scope
			if len(ctx.open) > 0 {
				ctx.buffer = append(ctx.buffer, line)
			}
			if loc := pattern.FindIndex(line.line); loc != nil {
				// get n-containing scopes and mark them for printing
				ctx.markNScopes(*nscopes, line_number, uint(loc[0]), uint(loc[1]))
			}
		}
		if len(ctx.open) == 0 {
			ctx.flushMatching(os.Stdout, false)
		}
		line_number++
	}
	ctx.flushMatching(os.Stdout, true)
}