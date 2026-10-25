import ghidra.app.script.GhidraScript;
import ghidra.app.decompiler.*;
import ghidra.program.model.listing.*;
import java.io.*;

// Read-only export from the existing library-only project. No target execution.
public class v08_Primitives extends GhidraScript {
 public void run() throws Exception {
  DecompInterface decompiler = new DecompInterface();
  decompiler.openProgram(currentProgram);
  String[] entries = {"001063a5", "001065e0", "001069ef", "00106b5d", "00106b7d", "00106c8c"};
  try (PrintWriter output = new PrintWriter(getScriptArgs()[0], "UTF-8")) {
   output.println("Static Ghidra export from fixed libv08.so.bin; no target execution.");
   for (String entry : entries) {
    Function function = getFunctionAt(toAddr(entry));
    output.println("\n=== " + entry + " ===");
    if (function == null) { output.println("MISSING"); continue; }
    DecompileResults result = decompiler.decompileFunction(function, 30, monitor);
    output.println(result.decompileCompleted() ? result.getDecompiledFunction().getC() : result.getErrorMessage());
   }
  }
  decompiler.dispose();
 }
}
