package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupLogWritesToDataFolder(t *testing.T) {
	dir := t.TempDir()
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })
	setupLog(dir)
	log.Print("startup line for the log file")
	b, err := os.ReadFile(filepath.Join(dir, "bddm.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "startup line for the log file") {
		t.Fatalf("bddm.log did not get the message:\n%s", b)
	}
}

func TestStartupMessagesDoNotNeedAConsole(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "fmt.Println") {
		t.Error("main.go should not print to the console (fmt.Println); use the log file so a GUI-subsystem build stays silent")
	}
	if strings.Contains(s, "Ctrl+C here") {
		t.Error("startup message should not tell the user to use Ctrl+C in a console window")
	}
	if !strings.Contains(s, "bddm.log") {
		t.Error("setupLog should write bddm.log in the data folder")
	}
}
