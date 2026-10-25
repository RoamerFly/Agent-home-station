// Offline static analysis only. Never launches the imported program.
import ghidra.app.script.GhidraScript;
import ghidra.app.decompiler.*;
import ghidra.program.model.listing.*;
import ghidra.program.model.address.*;
import java.io.*;
import java.nio.file.*;
public class MainAudit extends GhidraScript {
 public void run() throws Exception {
  String[] args=getScriptArgs();
  try(PrintWriter out=new PrintWriter(args[1],"UTF-8")) {
   out.println("Static Ghidra export; pseudocode is not original source. No target execution.");
   DecompInterface di=new DecompInterface();di.openProgram(currentProgram);
   for(String row:Files.readAllLines(Path.of(args[0]))) {
    if(row.isBlank())continue;String[] p=row.split("\t",2);Address a=toAddr(p[0]);
    Function f=getFunctionAt(a);if(f==null){disassemble(a);f=createFunction(a,null);}
    out.println("\n=== "+p[1]+" @ "+a+" ===");
    if(f==null){out.println("FUNCTION UNAVAILABLE");continue;}
    out.println("Ghidra identity: "+f.getName()+" body="+f.getBody());
    DecompileResults r=di.decompileFunction(f,25,monitor);
    if(r.decompileCompleted())out.println(r.getDecompiledFunction().getC());else out.println("DECOMPILE FAILED: "+r.getErrorMessage());
    InstructionIterator it=currentProgram.getListing().getInstructions(f.getBody(),true);
    while(it.hasNext()){Instruction ins=it.next();for(var ref:ins.getReferencesFrom())if(ref.getReferenceType().isCall())out.println("CALL "+ins.getAddress()+" -> "+ref.getToAddress()+" "+getSymbolAt(ref.getToAddress()));}
    out.flush();
   }
   di.dispose();
  }
 }
}
