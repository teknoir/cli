// Package textdiff makes small unified diffs for terminal output.
package textdiff

import (
	"fmt"
	"strings"
)

// Unified returns a unified diff of a and b with 3 lines of context, or "" when they are
// equal. aName and bName label the two sides.
func Unified(aName, bName, a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	ops := diffLines(al, bl)
	changed := false
	for _, o := range ops {
		if o.kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		return ""
	}
	const ctx = 3
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)

	// Group the ops into hunks: changes plus ctx lines around them.
	n := len(ops)
	i := 0
	for i < n {
		for i < n && ops[i].kind == ' ' {
			i++
		}
		if i >= n {
			break
		}
		start := i - ctx
		if start < 0 {
			start = 0
		}
		// extend while another change follows within 2*ctx unchanged lines
		end := i
		for end < n {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			gap := 0
			for end+gap < n && ops[end+gap].kind == ' ' {
				gap++
			}
			if end+gap >= n || gap > 2*ctx {
				if gap > ctx {
					gap = ctx
				}
				end += gap
				break
			}
			end += gap
		}
		if end > n {
			end = n
		}
		aStart, bStart, aLen, bLen := 0, 0, 0, 0
		for k := 0; k < start; k++ {
			if ops[k].kind != '+' {
				aStart++
			}
			if ops[k].kind != '-' {
				bStart++
			}
		}
		for k := start; k < end; k++ {
			if ops[k].kind != '+' {
				aLen++
			}
			if ops[k].kind != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", aStart+1, aLen, bStart+1, bLen)
		for k := start; k < end; k++ {
			sb.WriteByte(byte(ops[k].kind))
			sb.WriteString(ops[k].text)
			sb.WriteByte('\n')
		}
		i = end
	}
	return sb.String()
}

type op struct {
	kind byte // ' ', '-' or '+'
	text string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffLines is a longest-common-subsequence line diff.
func diffLines(a, b []string) []op {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}
