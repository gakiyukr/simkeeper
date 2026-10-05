package web

import "embed"

// tmplFS 内嵌全部页面模板，最终二进制不依赖外部文件。
//
//go:embed templates/*.html
var tmplFS embed.FS

// flagFS 内嵌国旗 SVG（来源与扩充方式见 flags/README.md）。
//
//go:embed flags/*.svg
var flagFS embed.FS
