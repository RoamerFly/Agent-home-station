"""Experimental bounded interpreter recovered from libv08's static rqtx_calc.

All addresses are synthetic byte-array offsets; no native code is executed.
The bundled data remains an opaque recovered VM program, not recovered source.
"""
import hashlib,json,pathlib,struct
ROOT=pathlib.Path(__file__).resolve().parents[2]
MASK=(1<<64)-1
def signed(x,bits=64):
    x&=(1<<bits)-1
    return x-(1<<bits) if x>>(bits-1) else x

def calculate(text,debug=False):
    if len(text)!=32 or any(c not in '0123456789abcdef' for c in text):raise ValueError('32 lower hex characters required')
    raw=bytes.fromhex((ROOT/'research/samples/rqtx.dat.bin').read_text().strip())
    table=raw[:512]
    code=bytearray(0x24000);code[:len(raw)-512]=raw[512:]
    state=bytearray(0x80440)
    result=bytearray(16)
    segments=[(0x100000,code),(0x200000,state),(0x400000,bytearray(text.encode())),(0x500000,result),(0x700000,bytearray(struct.pack('<I',0x54000000)))]
    def loc(a,n):
        for base,b in segments:
            if base<=a and a+n<=base+len(b):return b,a-base
        raise ValueError(f'unmapped VM address {a:x}+{n}')
    def read(a,n=8):
        b,p=loc(a,n);return int.from_bytes(b[p:p+n],'little')
    def write(a,x,n=8):
        b,p=loc(a,n);b[p:p+n]=(x&((1<<(n*8))-1)).to_bytes(n,'little')
    def reg(i):return read(0x200000+i*8)
    def put(i,x):write(0x200000+i*8,x)
    for dest,src in [(0x1c000,0x288),(0x1c0b8,0x210),(0x1c0c0,0x228),(0x1c0c8,0x240),(0x1c0d0,0x258),(0x1c0d8,0x270)]:
        write(0x100000+dest,0x100000+read(0x100000+src))
    put(0,0x400000);put(1,32);put(2,0x500000);put(3,0x500008)
    put(125,0x280400);put(127,0x700000);write(0x500008,4)
    pc=0x104000
    a=b=c=imm12=imm19=raw19=raw12=offset26=0
    histogram={}
    for step in range(2_000_000):
        ip=pc;ins=read(ip,4);pc=ip+4
        op=ins>>26
        if ip!=0x700000:
            op^=(ip>>8)&63
            if table[op*2]:op=table[op*2+1]
        histogram[op]=histogram.get(op,0)+1
        group=''
        if op<15:
            if op==0:raise ValueError('VM abort')
            if (1<<op)&0x15b4:group='R'
            elif (1<<op)&0x2a4a:group='I'
            elif (1<<op)&0x4000:group='U'
        elif op<28:
            if op in (15,16,17,22,23,24):group='I'
            elif op in (18,19,20,21,25,26):group='R'
            elif op==27:group='J'
        elif op<56:
            if (1<<op)&0x7c2abd10000000:group='R'
            elif (1<<op)&0x834142a0000000:group='I'
            elif (1<<op)&0x940040000000:group='U'
        if group in ('R','I','U'):a=((ins>>19)&127)^((ip>>2)&127)
        if group in ('R','I'):b=((ins>>12)^(ins>>19))&127
        if group=='R':c=((ins>>5)^(ins>>12))&127
        if group=='I':raw12=ins&4095;imm12=signed(raw12,12)
        if group=='U':raw19=ins&0x7ffff;imm19=signed(raw19,19)
        if group=='J':offset26=signed(ins&0x3ffffff,26)
        try:
            if op in (1,2):pc=reg(127)
            elif op==3:put(a,reg(b)+imm12)
            elif op in (4,5,19):put(a,int(signed(reg(c))<signed(reg(b))))
            elif op==6:put(a,reg(b)&(imm12&MASK))
            elif op in (7,8):put(a,reg(c)^reg(b))
            elif op==9:put(a,reg(b)%raw12)
            elif op in (10,11):put(a,reg(b)*imm12)
            elif op in (12,13):write((reg(b)+imm12)&MASK,reg(a))
            elif op==14:put(a,imm19)
            elif op in (15,16):put(a,reg(b)^(imm12&MASK))
            elif op==17:put(a,reg(b)-imm12)
            elif op==18:put(a,reg(b)-reg(c))
            elif op==20:put(a,int(reg(b)!=reg(c)))
            elif op==21:
                return (read(0x500000,4),step,histogram) if debug else read(0x500000,4)
            elif op==22:put(a,signed(read((reg(b)+imm12)&MASK,4),32))
            elif op==23:write((reg(b)+imm12)&MASK,reg(a),2)
            elif op in (24,25):put(a,signed(reg(b))>>(imm12&63))
            elif op==26:put(a,int(reg(b)==reg(c)))
            elif op==27:pc=(ip+offset26)&MASK
            elif op in (28,29,30):put(a,(reg(a)&~0x7ffff)^raw19)
            elif op in (31,32):put(a,reg(b)<<(imm12&63))
            elif op in (33,34):put(a,signed(read((reg(b)+imm12)&MASK,1),8))
            elif op in (35,36):put(a,reg(b)&reg(c))
            elif op==37:put(a,reg(b)|reg(c))
            elif op==38:put(a,read((reg(b)+imm12)&MASK))
            elif op==39:put(a,int(signed(reg(b))<signed(reg(c))))
            elif op in (40,41):write((reg(b)+imm12)&MASK,reg(a),4)
            elif op==42:
                if reg(a)==1:pc=(ip+imm19)&MASK
            elif op in (43,44):put(a,imm19*0x4000+(ip&~0x3fff))
            elif op==45:
                if reg(c)==1:put(a,reg(b))
            elif op==46:put(a,signed(read((reg(b)+imm12)&MASK,2),16))
            elif op==47:put(a,reg(a)+(raw19&0x3fff))
            elif op==48:write((reg(b)+imm12)&MASK,reg(a),1)
            elif op==49:put(a,reg(b)|(imm12&MASK))
            elif op in (50,51):target=reg(a);put(127,pc);pc=target
            elif op==52:put(a,reg(b))
            elif op in (53,54,55):put(a,reg(b)+reg(c))
            else:raise ValueError(f'unknown opcode {op}')
        except Exception as exc:
            raise ValueError(f'step={step} ip={ip:x} ins={ins:08x} op={op} group={group} a={a} b={b} c={c} imm12={imm12} imm19={imm19}: {exc}') from exc
    raise ValueError('VM instruction limit')

if __name__=='__main__':
    print(calculate('0'*32,True))
