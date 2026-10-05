# 国旗 SVG（内嵌静态资源）

- 来源：[flagcdn.com](https://flagcdn.com)，即 [flag-icons](https://github.com/lipis/flag-icons)（MIT 许可）项目的 CDN 分发。
- 文件名为 ISO 3166-1 alpha-2 小写代码，与 `handlers_numbers.go` 的 `Countries` 列表一一对应。
- 通过 `//go:embed flags/*.svg` 打进二进制。不用 Unicode 旗帜 emoji 的原因：Windows 浏览器没有旗帜 emoji 字形，🇯🇵 会退化成 "JP" 字母。
- 扩充国家时：下载 `https://flagcdn.com/<code>.svg` 存到本目录，并在 `Countries` 里登记对应条目；路由与模板按白名单（小写代码）校验。
