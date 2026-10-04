// tdata.go 支持用 Telegram Desktop 的 tdata 直接导入账号会话，
// 免去 tg-login 的验证码交互。tdata 等同账号完全访问权：
// 上传内容在内存中解析（不落盘），只把提取出的会话存进数据库。
package tgcall

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
)

// TDataImportLimit 上传 ZIP 的解压前大小上限。
// tdata 根目录的小文件只有几 KB；超过它说明用户把缓存目录也压进来了。
const TDataImportLimit = 32 << 20 // 32MB

// ImportTData 从上传的 ZIP（tdata 文件夹的压缩包）提取 MTProto 会话。
// passcode 是桌面端设置的本地密码；未设置则传空。
// 返回 base64 会话（可直接写入 notification_configs.tgcall_session）
// 与该会话的 Telegram 用户 ID。
//
// 注意：导入的会话与桌面端共享——在 Telegram 设置里「终止」那个桌面会话，
// 导入的会话会一并失效。
func ImportTData(zipBytes []byte, passcode string) (sessionB64 string, tgUserID uint64, err error) {
	if len(zipBytes) == 0 {
		return "", 0, fmt.Errorf("未收到上传文件")
	}
	if len(zipBytes) > TDataImportLimit {
		return "", 0, fmt.Errorf("文件超过 32MB：请只压缩 tdata 文件夹本身，不要包含 user_data 缓存")
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", 0, fmt.Errorf("不是有效的 ZIP 文件：请把 tdata 文件夹压缩成 zip 后上传")
	}
	root, err := locateTDataRoot(zr)
	if err != nil {
		return "", 0, err
	}
	accounts, err := tdesktop.ReadFS(root, []byte(passcode))
	if err != nil {
		if len(passcode) > 0 {
			return "", 0, fmt.Errorf("tdata 解密失败：请确认是已登录桌面的 tdata，且本地密码正确（%v）", err)
		}
		return "", 0, fmt.Errorf("tdata 解析失败：请确认压缩包内是已登录桌面端的 tdata 文件夹（%v）", err)
	}
	if len(accounts) == 0 {
		return "", 0, fmt.Errorf("tdata 里没有已登录的账号")
	}
	// 多账号桌面取第一个；需要别的账号时，可在桌面端把目标账号排到首位重新导出
	sessionB64, err = accountToSession(accounts[0])
	if err != nil {
		return "", 0, err
	}
	return sessionB64, accounts[0].Authorization.UserID, nil
}

// locateTDataRoot 在 ZIP 里找到包含 key_data（key_datas/key_data.0 等）的目录，
// 返回以其为根的 fs.FS。兼容「直接压缩 tdata 内容」和「带一层或多层父目录」两种结构。
func locateTDataRoot(zr *zip.Reader) (fs.FS, error) {
	type candidate struct{ prefix string }
	var found []candidate
	seen := map[string]bool{}
	for _, f := range zr.File {
		name := strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "/")
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		if strings.HasPrefix(path.Base(name), "key_data") {
			prefix := path.Dir(name)
			if prefix == "." {
				prefix = ""
			}
			if !seen[prefix] {
				seen[prefix] = true
				found = append(found, candidate{prefix: prefix})
			}
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("压缩包里没找到 tdata（应包含 key_data 文件）：请压缩 tdata 文件夹本身")
	}
	// 取路径最浅的候选（用户把整个 Telegram 目录压进来时，tdata 层级更浅）
	sort.Slice(found, func(i, j int) bool {
		return strings.Count(found[i].prefix, "/") < strings.Count(found[j].prefix, "/")
	})
	if found[0].prefix == "" {
		return zr, nil
	}
	return fs.Sub(zr, found[0].prefix)
}

// accountToSession 把 tdesktop 账号转成 gotd 会话存储格式（base64 的版本化 JSON）。
func accountToSession(a tdesktop.Account) (string, error) {
	key, ok := a.Authorization.Keys[a.Authorization.MainDC]
	if !ok {
		// MainDC 没有对应密钥时退回任一可用密钥（gotd 连上后会自行校正 DC）
		for _, k := range a.Authorization.Keys {
			key = k
			ok = true
			break
		}
	}
	if !ok {
		return "", fmt.Errorf("tdata 里没有可用的授权密钥")
	}
	keyID := sha1.Sum(key[:]) // MTProto auth_key_id = SHA1(auth_key) 的前 12 字节
	data := session.Data{
		DC:        a.Authorization.MainDC,
		AuthKey:   key[:],
		AuthKeyID: keyID[:12],
	}
	buf, err := json.Marshal(sessionJSON{Version: 1, Data: data})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// sessionJSON 与 gotd session.Loader 的存储格式（jsonData）字段完全一致；
// 类型未导出，这里按相同字段形状重建以保证序列化兼容。
type sessionJSON struct {
	Version int
	Data    session.Data
}
