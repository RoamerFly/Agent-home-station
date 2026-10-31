// wechat-plugin validates packages without installing or executing them.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"wechat-bot/internal/pluginhost"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "verify" {
		fmt.Fprintln(os.Stderr, "usage: wechat-plugin verify <package-directory>")
		os.Exit(2)
	}
	p, err := pluginhost.Verify(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"id": p.ID, "version": p.Version, "status": p.Status, "files_verified": len(p.Files), "inspection_only": true, "installed": false, "executed": false})
}
