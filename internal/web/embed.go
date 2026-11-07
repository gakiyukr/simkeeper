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

// assetFS 内嵌前端静态资源（Flowbite 定制构建产物；重建方式见 ui-build/input.css）。
//
//go:embed assets/*.css assets/*.js
var assetFS embed.FS
