package main

import (
	"fmt"
	"regexp"
	"strings"
)

func maskLocalPaths(text string) string {
	home := `C:\Users\Administrator`
	maskWindows := regexp.MustCompile(`(?i)([a-z]:[\\/])([^\s"',]+)`)
	text = maskWindows.ReplaceAllStringFunc(text, func(m string) string {
		if home != "" && strings.HasPrefix(strings.ToLower(m), strings.ToLower(home)) {
			rest := strings.TrimLeft(m[len(home):], `/\`)
			return "~/" + strings.ReplaceAll(rest, `\`, "/")
		}
		idx := strings.IndexAny(m, `/\`)
		if idx < 0 {
			return m
		}
		rest := strings.TrimLeft(m[idx+1:], `/\`)
		return "~/" + strings.ReplaceAll(rest, `\`, "/")
	})
	return text
}

func main() {
	in := "请读取文件 C:\\Users\\Administrator\\Desktop\\testfile.txt 的内容"
	fmt.Println("in :", in)
	fmt.Println("out:", maskLocalPaths(in))
}