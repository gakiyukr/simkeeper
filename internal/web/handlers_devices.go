// handlers_devices.go 设备管理：记录 eSIM 安装进了什么设备。
package web

import (
	"net/http"
	"strconv"
	"strings"

	"simkeeper/internal/store"
)

// deviceTypes 设备类型白名单（存储键）。
var deviceTypes = []string{"phone", "tablet", "watch", "modem", "other"}

// deviceTypeText 设备类型的展示名。
func deviceTypeText(t string) string {
	switch t {
	case "phone":
		return "手机"
	case "tablet":
		return "平板"
	case "watch":
		return "手表"
	case "modem":
		return "随身 WiFi"
	}
	return "其他"
}

func validDeviceType(t string) bool {
	for _, v := range deviceTypes {
		if v == t {
			return true
		}
	}
	return false
}

// DeviceView 设备卡片：设备本体 + 名下安装的 eSIM。
type DeviceView struct {
	Device  store.Device
	Numbers []store.PhoneNumber
}

// DevicesPage 设备管理页数据。
type DevicesPage struct {
	Devices []DeviceView
}

// HandleDevices 设备管理页（GET 展示 / POST 添加）。
func (a *App) HandleDevices(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "设备管理")
	d.ActiveNav = "devices"

	if r.Method == http.MethodPost {
		a.deviceCreate(w, r, u.ID)
		return
	}

	devices, err := a.Devices.ListForUser(u.ID)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	numbers, _, _ := a.Numbers.ListForUser(u.ID, 1, 1000000)
	views := make([]DeviceView, 0, len(devices))
	for _, dev := range devices {
		v := DeviceView{Device: dev}
		for _, n := range numbers {
			if n.DeviceID == dev.ID {
				v.Numbers = append(v.Numbers, n)
			}
		}
		views = append(views, v)
	}
	d.Content = DevicesPage{Devices: views}
	a.render(w, http.StatusOK, "page_devices", d)
}

// parseDeviceForm 读取并校验设备表单；返回名称、类型、备注与错误信息。
func parseDeviceForm(r *http.Request) (name, dtype, notes, errMsg string) {
	name = strings.TrimSpace(r.PostFormValue("name"))
	dtype = r.PostFormValue("device_type")
	notes = strings.TrimSpace(r.PostFormValue("notes"))
	switch {
	case name == "":
		errMsg = "请填写设备名称"
	case len([]rune(name)) > 100:
		errMsg = "设备名称过长（100 字以内）"
	case !validDeviceType(dtype):
		dtype = "phone"
	case len([]rune(notes)) > 2000:
		errMsg = "备注过长（2000 字以内）"
	}
	return
}

func (a *App) deviceCreate(w http.ResponseWriter, r *http.Request, userID int64) {
	name, dtype, notes, errMsg := parseDeviceForm(r)
	if errMsg != "" {
		a.setFlash(w, errMsg, true)
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
		return
	}
	if _, err := a.Devices.Create(&store.Device{UserID: userID, Name: name, DeviceType: dtype, Notes: notes}); err != nil {
		a.setFlash(w, "添加失败，请重试", true)
	} else {
		a.setFlash(w, "设备已添加", false)
	}
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
}

// HandleDeviceEdit 更新设备（POST + CSRF）。
func (a *App) HandleDeviceEdit(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	back := func(msg string, isErr bool) {
		a.setFlash(w, msg, isErr)
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	dev, err := a.Devices.ByID(id, u.ID)
	if err != nil {
		back("设备不存在", true)
		return
	}
	name, dtype, notes, errMsg := parseDeviceForm(r)
	if errMsg != "" {
		back(errMsg, true)
		return
	}
	dev.Name, dev.DeviceType, dev.Notes = name, dtype, notes
	if err := a.Devices.Update(dev); err != nil {
		back("保存失败，请重试", true)
		return
	}
	back("设备已更新", false)
}

// HandleDeviceDelete 删除设备（名下号码变为未指定安装）。
func (a *App) HandleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	back := func(msg string, isErr bool) {
		a.setFlash(w, msg, isErr)
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := a.Devices.ByID(id, u.ID); err != nil {
		back("设备不存在", true)
		return
	}
	if err := a.Devices.Delete(id, u.ID); err != nil {
		back("删除失败，请重试", true)
		return
	}
	back("设备已删除，其名下号码变为未指定安装", false)
}
