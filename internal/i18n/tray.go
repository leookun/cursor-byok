package i18n

import "os"

var trayLang = detectLang()

func detectLang() string {
	lang := os.Getenv("LANG")
	if lang == "" {
		lang = os.Getenv("LC_ALL")
	}
	if lang == "" {
		lang = os.Getenv("LANGUAGE")
	}
	if len(lang) >= 2 {
		switch lang[:2] {
		case "zh":
			return "zh"
		case "ru":
			return "ru"
		default:
			return "en"
		}
	}
	return "en"
}

type trayStrings struct {
	StatusStopped  string
	StatusRunning  string
	StartService   string
	StopService    string
	CheckUpdates   string
	ShowWindow     string
	HideWindow     string
	Quit           string
}

var trayStringsMap = map[string]trayStrings{
	"en": {
		StatusStopped:  "Status: Stopped",
		StatusRunning:  "Status: Running",
		StartService:   "Start Service",
		StopService:    "Stop Service",
		CheckUpdates:   "Check Updates",
		ShowWindow:     "Show Window",
		HideWindow:     "Hide Window",
		Quit:           "Quit",
	},
	"zh": {
		StatusStopped:  "状态：未启动",
		StatusRunning:  "状态：运行中",
		StartService:   "启动服务",
		StopService:    "停止服务",
		CheckUpdates:   "检查更新",
		ShowWindow:     "显示窗口",
		HideWindow:     "隐藏窗口",
		Quit:           "退出",
	},
	"ru": {
		StatusStopped:  "Статус: Остановлен",
		StatusRunning:  "Статус: Работает",
		StartService:   "Запустить сервис",
		StopService:    "Остановить сервис",
		CheckUpdates:   "Проверить обновления",
		ShowWindow:     "Показать окно",
		HideWindow:     "Скрыть окно",
		Quit:           "Выход",
	},
}

func Tray() trayStrings {
	if s, ok := trayStringsMap[trayLang]; ok {
		return s
	}
	return trayStringsMap["en"]
}
