package main
import("debug/elf";"debug/gosym";"encoding/binary";"encoding/json";"os";"fmt")
func main(){ f,e:=elf.Open(os.Args[1]);if e!=nil{panic(e)};defer f.Close(); p:=f.Section(".gopclntab"); if p==nil{panic("no pclntab")}; b,e:=p.Data();if e!=nil{panic(e)}; textStart:=binary.LittleEndian.Uint64(b[24:32]); tab,e:=gosym.NewTable(nil,gosym.NewLineTable(b,textStart));if e!=nil{panic(e)};type Row struct{Name string;Entry string;End string};rows:=[]Row{};for _,fn:=range tab.Funcs{rows=append(rows,Row{fn.Name,fmt.Sprintf("0x%x",fn.Entry),fmt.Sprintf("0x%x",fn.End)})};json.NewEncoder(os.Stdout).Encode(rows)}

