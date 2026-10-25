import ghidra.app.script.GhidraScript;
import ghidra.app.decompiler.*;
import ghidra.program.model.listing.*;
import java.io.*;
public class LibAudit extends GhidraScript {
 public void run() throws Exception {
  DecompInterface d = new DecompInterface(); d.openProgram(currentProgram);
  String[] names={"rqtx","rqtx_calc","__init","rqtx_init","si","encode_string","decode_string"};
  File out=new File(getScriptArgs()[0]);
  try(PrintWriter w=new PrintWriter(out,"UTF-8")) {
   for(String name:names){
    Function found=null;
    for(Function f:currentProgram.getFunctionManager().getFunctions(true)) if(f.getName().equals(name)){found=f;break;}
    if(name.equals("rqtx_calc")) found=getFunctionAt(toAddr("001077a1")); if(name.equals("__init")) found=getFunctionAt(toAddr("0010916b")); w.println("\n=== "+name+" ===");
    if(found==null){w.println("NOT FOUND");continue;}
    w.println("address="+found.getEntryPoint());
    DecompileResults res=d.decompileFunction(found,30,monitor);
    if(res.decompileCompleted()) w.println(res.getDecompiledFunction().getC()); else w.println("FAILED: "+res.getErrorMessage());
   }
  } d.dispose();
 }
}


