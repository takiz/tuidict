package main

//go:generate gotext -srclang=en update -out=catalog.go -lang=en,ru

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/famz/SetLocale"
	"github.com/gdamore/tcell"
	"github.com/rivo/tview"
	"github.com/takiz/gostardict/stardict"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	_ "golang.org/x/text/message/catalog"
)

const (
	VERSION    = "0.1"
	runeStatus = " " + string('\U0001f50a')
)

const (
	SAVE = iota
	LOAD
	DICT
	HISTORY
)

// For printing hotkeys.
type Key struct {
	Name string
	Desc string
}

var (
	App              *tview.Application
	txt              *tview.TextView
	inputField       *tview.InputField
	Hotkeys          *tview.TextView
	SoundStatus      *tview.TextView
	MainGrid         *tview.Grid
	Rep              *strings.Replacer
	FoundDict        []string // list of dictionaries in which the word is found.
	History          = make(map[string]int)
	SoundDirs        []string // Sound subdirectories.
	SoundPath        string
	PlayPath         string
	LastWord         string
	HistorySize      int
	NoAutocompletion bool
	Input            = true // inputField active.
	Player           []string
	SdcvArgs         []string
	lastSearch       string
	DictPath         string
	configDir        string
)

func SetOpts() {
	flag.IntVar(&HistorySize, "history-size", 10, P("Set history size"))
	sound := flag.String("sound-dir", "/usr/share/stardict/sounds", P("Set the directory with sound files"))
	dict := flag.String("dict-dir", "/usr/share/stardict/dic", P("Set the directory with dictionary files"))
	player := flag.String("player", "mpv", P("Set audio player"))
	sdcv := flag.String("sdcv", "-c -n", P("Set sdcv custom arguments"))
	flag.BoolVar(&NoAutocompletion, "noauto", false, P("Disable autocompletion"))
	version := flag.Bool("version", false, P("Print current version"))

	flag.Parse()
	SoundPath = strings.TrimSuffix(*sound, "/") + "/"
	DictPath = strings.TrimSuffix(*dict, "/") + "/"
	SdcvArgs = strings.Split(*sdcv, " ")
	Player = strings.Split(*player, " ")
	var err error
	if _, err = exec.LookPath("sdcv"); err != nil {
		log.Fatal(err)
	}
	if configDir, err = os.UserConfigDir(); err != nil {
		log.Fatal(err)
	}
	configDir += "/tuidict/"
	if err := os.MkdirAll(configDir, 0774); err != nil {
		log.Fatal(err)
	}
	if os.Getenv("TUIDICT_NOAUTO") == "1" {
		NoAutocompletion = true
	}
	SoundFiles, err := ioutil.ReadDir(SoundPath)
	if err != nil {
		panic(err)
	}
	SoundDirs = make([]string, 0, len(SoundFiles))
	for _, file := range SoundFiles {
		if file.IsDir() {
			SoundDirs = append(SoundDirs, file.Name()+"/")
		}
	}
	if *version {
		fmt.Println(VERSION)
		os.Exit(0)
	}
}

func main() {
	SetLocales()
	SetOpts()
	SetHistory(LOAD)

	inputField = NewInputFieldPrim("[-:-]" + P("Enter a word or phrase") + ": [-:-]")
	if !NoAutocompletion {
		SetAutocompletion()
	}
	wel := P(` Welcome!

 Arrows/PageUp/PageDown/Home/End — text scrolling.
 F1 or Ctrl+X — quit.
 F2 or Ctrl+E — change focus between main input field and text.
 F3 or Ctrl+D — dictionaries navigation if a word or phrase are founded.
 F4 or Ctrl+H — history.
 F5 or Ctrl+P — pronounce word if it exists in the installed sound base (see the color of the indicator next to the input field).
 F7 or Ctrl+F — search in the text.
 Use the -h command line flag for more information.`)

	txt = NewTextPrim(wel).SetRegions(true).SetWordWrap(true)
	txt.SetBorder(true).SetBorderColor(tcell.ColorDefault)

	MainKeysText := FormatKeys([]Key{
		{"F1", P("Quit")}, {"F2", P("Change focus")},
		{"F3", P("Dictionaries")}, {"F4", P("History")},
		{"F5", P("Pronounce")}, {"F7", P("Search")}})

	Hotkeys = NewTextPrim(MainKeysText)
	SoundStatus = NewTextPrim("[gray:]" + runeStatus + "[-:-]")

	MainGrid = tview.NewGrid().
		SetRows(1, 0, 1).
		SetColumns(30, 30, 0).
		SetBorders(false).
		AddItem(inputField, 0, 0, 1, 2, 0, 0, true).
		AddItem(SoundStatus, 0, 2, 1, 1, 0, 0, false).
		AddItem(txt, 1, 0, 1, 3, 0, 0, false).
		AddItem(Hotkeys, 2, 0, 1, 3, 0, 0, false)

	MainGrid.SetBackgroundColor(tcell.ColorDefault)
	App = tview.NewApplication().SetRoot(MainGrid, true)
	App.SetBeforeDrawFunc(func(s tcell.Screen) bool {
		s.Clear()
		return false
	})
	App.SetFocus(inputField)
	SetMainInput()
	request := flag.Args()
	if len(request) > 0 {
		inputField.SetText(strings.Join(request, " "))
		FindWord()
	}
	if err := App.Run(); err != nil {
		panic(err)
	}
}

func SetMainInput() {
	App.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyF1, tcell.KeyCtrlX:
			App.Stop()
			SetHistory(SAVE)
		case tcell.KeyF2, tcell.KeyCtrlE:
			if Input {
				App.SetFocus(txt)
				Input = false
			} else {
				App.SetFocus(inputField)
				Input = true
			}
		case tcell.KeyF3, tcell.KeyCtrlD:
			ShowDropdown(DICT)
		case tcell.KeyF4, tcell.KeyCtrlH:
			ShowDropdown(HISTORY)
		case tcell.KeyF5, tcell.KeyCtrlP:
			SoundPlay()
		case tcell.KeyEnter:
			FindWord()
		case tcell.KeyF7, tcell.KeyCtrlF:
			ShowSearchInput(&lastSearch)
		}
		return event
	})
}

func NewInputFieldPrim(label string) *tview.InputField {
	inp := tview.NewInputField().
		SetLabel(label).
		SetFieldWidth(40).SetFieldTextColor(tcell.Color221).
		SetFieldBackgroundColor(tcell.Color25)
	inp.SetBackgroundColor(tcell.ColorDefault)
	return inp
}

func NewTextPrim(text string) *tview.TextView {
	t := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetText(text).SetTextColor(tcell.ColorDefault)
	t.SetBackgroundColor(tcell.ColorDefault)
	return t
}

func HighlightText(next int, new bool) int {
	if new {
		next = len(FoundDict)
	} else {
		next += 1
	}
	txt.Highlight(strconv.Itoa(next)).ScrollToHighlight()
	return next + 1
}

func ShowSearchInput(last *string) {
	var next int
	text := txt.GetText(false)
	MainGrid.RemoveItem(Hotkeys)
	focus := App.GetFocus()
	keys := FormatKeys([]Key{{"Esc", P("Close")}, {"F3", P("Next")}})
	inp := NewInputFieldPrim(keys + "[-:-] " + P("Search") + ": [-:-]").SetText(*last)
	endwin := func() {
		MainGrid.RemoveItem(inp)
		MainGrid.AddItem(Hotkeys, 2, 0, 1, 3, 0, 0, false)
	}
	MainGrid.AddItem(inp, 2, 0, 1, 3, 0, 0, true)
	App.SetFocus(inp).
		SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyEsc:
				if len(text) > 0 {
					txt.SetText(text)
				}
				endwin()
				App.SetFocus(focus)
				SetMainInput()
			case tcell.KeyF1, tcell.KeyCtrlX:
				App.Stop()
				SetHistory(SAVE)
			case tcell.KeyF3, tcell.KeyCtrlN:
				next = HighlightText(next, false)
			case tcell.KeyEnter:
				s := inp.GetText()
				if len(s) > 0 {
					next = SearchText(text, s)
				}
				*last = s
			}
			return event
		})
}

func SearchText(text, req string) int {
	var res string
	s := strings.ToLower(req)
	sc := bufio.NewScanner(strings.NewReader(text))
	n := len(FoundDict)
	i := n
	for sc.Scan() {
		t := sc.Text()
		if strings.Contains(t, s) {
			res += strings.ReplaceAll(t, s,
				fmt.Sprintf("[#FCE94F:#FF5FFF][\"%d\"]%s[\"\"][-:-]", i, s)) + "\n"
			i++
		} else {
			res += t + "\n"
		}
	}
	txt.SetText(res)
	HighlightText(0, true)
	return n
}

func FindWord() {
	if !Input {
		return
	}
	text := inputField.GetText()
	n := len(History)
	History[text] = n
	if n >= HistorySize {
		for k, v := range History {
			if v == 0 {
				delete(History, k)
			} else {
				History[k] = v - 1
			}
		}
	}
	GetSdcv(text)
	txt.ScrollToBeginning()
	App.SetFocus(txt)
	Input = false
}

func FormatKeys(keys []Key) string {
	res := "[-:-]"
	for _, k := range keys {
		res += k.Name + "[#E6DB58:#3465A4]" + " " + k.Desc + " [-:-]"
	}
	return res
}

// If dictionaries hashes are changed then need to update cache for autocompletion.
func CheckHash() bool {
	var save bool
	var f *os.File
	var err error
	hash := make([]byte, 0, 1)
	hashCurrent := make([]byte, 0, 1)
	d := configDir + "dicts"
	if _, err = os.Stat(d); err != nil {
		f, err = os.OpenFile(d, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		save = true
		defer func() {
			if err := f.Close(); err != nil {
				log.Fatal(err)
			}
		}()
	} else {
		hash, err = ioutil.ReadFile(d)
	}
	if err != nil {
		log.Fatal(err)
	}

	files, err := ioutil.ReadDir(DictPath)
	if err != nil {
		log.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".ifo") {
			h := md5.Sum([]byte(file.Name()))
			hashCurrent = append(hashCurrent, h[:]...)
		}
	}
	if save {
		if err := ioutil.WriteFile(d, hashCurrent, 0644); err != nil {
			log.Fatal(err)
		}
	} else if bytes.Compare(hash, hashCurrent) != 0 {
		// CacheWords()
		return false
	}
	return true
}

func SetAutocompletion() {
	var cur string
	var words []string
	var end bool

	d := configDir + "words"
	if _, err := os.Stat(d); !CheckHash() || err != nil {
		fmt.Fprintf(os.Stdout, "%s\n", P("Creating autocompletion cache... Please, wait."))
		CacheWords()
	}

	file, err := os.Open(d)
	if err != nil {
		panic(err)
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		words = append(words, scanner.Text())
	}
	file.Close()

	inputField.SetAutocompleteFunc(func(currentText string) (entries []string) {
		if len(currentText) == 0 {
			return
		}
		if end && strings.HasPrefix(strings.ToLower(currentText), strings.ToLower(cur)) {
			return
		}
		i := 0
		j := 0
		flag := false
		for n, word := range words {
			if i > 25 {
				break
			}
			if strings.HasPrefix(strings.ToLower(word), strings.ToLower(currentText)) {
				entries = append(entries, word)
				i++
				flag = true
				j = n
			} else if flag && n-j > 2 {
				break
			}

		}
		if len(entries) <= 1 {
			entries = nil
			end = true
			inputField.SetFieldBackgroundColor(tcell.ColorRed)
			cur = currentText
		} else {
			end = false
			inputField.SetFieldBackgroundColor(tcell.Color25)
		}
		return
	})
}

func SetHistory(r int) {
	var f *os.File
	var err error
	d := configDir + "history"
	if r == SAVE {
		f, err = os.OpenFile(d, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	} else {
		f, err = os.OpenFile(d, os.O_RDONLY|os.O_CREATE, 0644)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	}()

	if r == SAVE {
		for k, _ := range History {
			fmt.Fprintf(f, "%s\n", k)
		}
		return
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		History[sc.Text()] = len(History)
	}
}

func SoundCheck() bool {
	if len(SoundDirs) == 0 || len(LastWord) == 0 {
		return false
	}
	PlayPath = ""
	check := func(p string) bool {
		if _, err := os.Stat(p); err == nil {
			PlayPath = p
			return true
		}
		return false
	}
	for _, d := range SoundDirs {
		str := strings.ToLower(strings.TrimSpace(LastWord))
		s := string([]rune(str)[0])
		Path := filepath.Join(SoundPath, d, s, str)
		if check(Path + ".mp3") {
			return true
		} else if check(Path + ".ogg") {
			return true
		} else if check(Path + ".wav") {
			return true
		}
	}
	return false
}

func SoundPlay() {
	if len(PlayPath) == 0 {
		return
	}
	if _, err := exec.LookPath(Player[0]); err != nil {
		ShowErrorInfo(err)
		return
	}
	var err error
	if _, err = os.Stat(PlayPath); err == nil {
		p := append(Player, PlayPath)
		cmd := exec.Command(p[0], p[1:]...)
		if err := cmd.Start(); err != nil {
			ShowErrorInfo(err)
			return
		}
	}
}

func ShowErrorInfo(err error) {
	focus := App.GetFocus()
	modal := tview.NewModal().SetText(fmt.Sprintln(err)).
		AddButtons([]string{"OK"})
	modal.SetDoneFunc(func(buttonIndex int, buttonLabel string) {
		MainGrid.RemoveItem(modal)
		App.SetFocus(focus)
	})
	MainGrid.AddItem(modal, 1, 0, 1, 3, 0, 0, false)
	App.SetFocus(modal)
}

func ShowDropdown(r int) {
	if r == HISTORY {
		if len(History) == 0 {
			return
		}
	} else {
		if len(FoundDict) == 0 {
			return
		}
	}
	MainGrid.RemoveItem(Hotkeys)
	focus := App.GetFocus()
	opts := make([]string, 0, 1)
	var labelText string
	keys := FormatKeys([]Key{{"Esc", P("Close")}, {"Arrows/Enter", P("Select")}})
	if r == HISTORY {
		labelText = keys + "[-:-] " + P("History") + ": [-:-]"
		for k, _ := range History {
			opts = append(opts, k)
		}
	} else {
		labelText = keys + "[-:-] " + P("Found in dictionaries") + ": [-:-]"
		opts = FoundDict
	}
	dropdown := tview.NewDropDown().
		SetLabel(labelText).
		SetFieldBackgroundColor(tcell.Color25).
		SetFieldTextColor(tcell.Color221).
		SetOptions(opts, nil).
		SetCurrentOption(0)
	endwin := func() {
		MainGrid.RemoveItem(dropdown)
		MainGrid.AddItem(Hotkeys, 2, 0, 1, 3, 0, 0, false)
	}
	dropdown.SetBackgroundColor(tcell.Color221)
	dropdown.SetSelectedFunc(func(text string, index int) {
		cur, text := dropdown.GetCurrentOption()
		if cur != -1 {
			if r == HISTORY {
				txt.Clear()
				inputField.SetText(text)
				GetSdcv(text)
				txt.ScrollToBeginning()
			} else {
				txt.Highlight(strconv.Itoa(cur)).ScrollToHighlight()
			}
		}
		endwin()
		App.SetFocus(txt)
		Input = false
		SetMainInput()
	})
	MainGrid.AddItem(dropdown, 2, 0, 1, 3, 0, 0, true)
	App.SetFocus(dropdown).
		SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyF1, tcell.KeyCtrlX:
				App.Stop()
				SetHistory(SAVE)
			case tcell.KeyEsc:
				endwin()
				App.SetFocus(focus)
				SetMainInput()
			}
			return event
		})
}

func CacheWords() {
	var Words = make(map[string]int)

	getWord := func(fname string) {
		dict, err := stardict.NewDictionary(DictPath, fname)
		if err != nil {
			panic(err)
		}
		idx := dict.GetIdx()
		for k, _ := range idx.Items {
			Words[k] = 0
		}
	}
	files, err := ioutil.ReadDir(DictPath)
	if err != nil {
		panic(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".ifo") {
			getWord(strings.TrimSuffix(file.Name(), ".ifo"))
		}
	}

	f, err := os.OpenFile(configDir+"words", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatal(err)
	}
	srtWords := make([]string, 0, len(Words))
	for k, _ := range Words {
		srtWords = append(srtWords, k)
	}
	sort.Slice(srtWords, func(i, j int) bool {
		return strings.ToLower(srtWords[i]) < strings.ToLower(srtWords[j])
	})
	for _, k2 := range srtWords {
		if _, err = fmt.Fprintln(f, k2); err != nil {
			f.Close()
			log.Fatal(err)
		}
	}
	if err = f.Close(); err != nil {
		log.Fatal(err)
	}
}

var lng *message.Printer

func P(text string) string {
	return lng.Sprintf(text)
}

func SetLocales() {
	l := os.Getenv("LANG")
	if l == "" || l == "C" {
		os.Setenv("LANG", "en")
	}
	if os.Getenv("LC_NUMERIC") == "" {
		os.Setenv("LC_NUMERIC", "C")
	}
	SetLocale.SetLocale(SetLocale.LC_ALL, "")
	SetLocale.SetLocale(SetLocale.LC_NUMERIC, "C")

	matcher := language.NewMatcher(message.DefaultCatalog.Languages())
	lang := strings.Split(os.Getenv("LANG"), ".")
	langTag, _, _ := matcher.Match(language.MustParse(lang[0]))
	lng = message.NewPrinter(langTag)
}

func GetSdcv(s string) {
	var out bytes.Buffer
	FoundDict = make([]string, 0, 1)
	LastWord = s
	args := append(SdcvArgs, s)
	cmd := exec.Command("sdcv", args...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		fmt.Println(err)
		return
	}
	Rep = strings.NewReplacer("[0;34m", "", "[0m", "")
	scanner := bufio.NewScanner(&out)
	var res string
	old := "-->"
	i := 0
	j := 0
	for scanner.Scan() {
		t := scanner.Text()
		y := strings.Contains(t, old)
		if y && i%2 == 0 {
			res += strings.Replace(t, old,
				fmt.Sprintf("[#06989A:][\"%d\"]%s[\"\"][-:-]", j, old), 1) + "\n"

			FoundDict = append(FoundDict, Rep.Replace(t[3:]))
			j++
		} else {
			res += t + "\n"
		}
		if y {
			i++
		}
	}
	Rep = strings.NewReplacer("[0;37m", "[0;10m", "[3m", "[0;10m",
		"[0;34m", "[0;36m")
	txt.SetText(tview.TranslateANSI(Rep.Replace(res)))
	if SoundCheck() {
		SoundStatus.SetText("[green:]" + runeStatus + "[-:-]")
	} else {
		SoundStatus.SetText("[gray:]" + runeStatus + "[-:-]")
	}
}
