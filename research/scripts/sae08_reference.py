"""Independent Python model of static table data flow; never executes the target.

Produces public synthetic-input vectors, not runtime-oracle/native parity claims.
The Go implementation uses flat arrays; this model expands to multidimensional
tables and pairwise nibble truth tables for a differently structured comparison.
"""
import hashlib, json, pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]

def read_fields(data):
    def integer(pos):
        x=0
        for i in range(10):
            v=data[pos+i]; x |= (v&127)<<(7*i)
            if v<128:return x,pos+i+1
        raise ValueError('varint')
    result={}; pos=0
    while pos<len(data):
        tag,pos=integer(pos)
        if tag&7==0:result[tag>>3],pos=integer(pos)
        elif tag&7==2:
            size,pos=integer(pos);result[tag>>3]=data[pos:pos+size];pos+=size
        else:raise ValueError('wire')
    return result

class Reference:
    def __init__(self, data):
        f=read_fields(data)
        assert f[1]==b'00000008' and f[3]==0x3060
        self.iv=f[2]
        self.expansion=[]; self.combine=[]
        for r in range(9):
            round_exp=[]; round_comb=[]
            for row in range(4):
                row_exp=[]; row_comb=[]
                for col in range(4):
                    cell=[]
                    for x in range(256):
                        cell.append(bytes(f[10][r*16384+row*4096+col*1024+x*4+k]^f[9][(r*row*col+x+k+7)%128] for k in range(4)))
                    row_exp.append(cell)
                    row_comb.append([bytes(f[12][r*12288+row*3072+col*768+stage*256+half*128+x]^f[11][(r*row*col+stage*half+x+23)%64] for half in range(2) for x in range(128)) for stage in range(3)])
                round_exp.append(row_exp);round_comb.append(row_comb)
            self.expansion.append(round_exp);self.combine.append(round_comb)
        self.final=[f[18][i:i+256] for i in range(0,4096,256)]

    def encrypt(self, plain, full=False):
        assert plain
        pad=16-len(plain)%16
        if full or pad!=16:plain+=bytes([pad])*pad
        previous=self.iv; result=bytearray()
        for off in range(0,len(plain),16):
            block=bytes(a^b for a,b in zip(plain[off:off+16],previous))
            state=[[block[c*4+r] for c in range(4)] for r in range(4)]
            for rnd in range(10):
                state=[line[r:]+line[:r] for r,line in enumerate(state)]
                if rnd==9:break
                vectors=[[self.expansion[rnd][r][c][state[r][c]] for c in range(4)] for r in range(4)]
                new=[[0]*4 for _ in range(4)]
                for r in range(4):
                    for c in range(4):
                        acc=vectors[3][c][r]
                        for source in (2,1,0):
                            left=vectors[source][c][r]
                            table=self.combine[rnd][r][c][source]
                            hi=((left>>4)<<4)|(acc>>4)
                            lo=((left&15)<<4)|(acc&15)
                            def nibble(idx,base):return (table[base+idx%128]>>(4*(idx//128)))&15
                            acc=(nibble(hi,0)<<4)|nibble(lo,128)
                        new[r][c]=acc
                state=new
            previous=bytes(self.final[r*4+c][state[r][c]] for c in range(4) for r in range(4))
            result.extend(previous)
        return bytes(result)

if __name__=='__main__':
    sample=(ROOT/'research/samples/08sae.dat.bin').read_bytes()
    assert hashlib.sha256(sample).hexdigest()=='dfedf50d53e3579691bf428fa9686883ffe91b9781fd587e08e9808617a152a3'
    ref=Reference(sample)
    vectors=[]
    for length in (1,15,16,17,31,32,33,64,255,256,257):
        plain=bytes((i*73+19)%256 for i in range(length))
        vectors.append({'plain_hex':plain.hex(),'zip_hex':ref.encrypt(plain).hex(),'zt_hex':ref.encrypt(plain,True).hex()})
    evidence={'scope':'Independent Python static-data-flow reconstruction; not target execution or native runtime parity', 'sample_sha256':hashlib.sha256(sample).hexdigest(),'vectors':vectors}
    (ROOT/'research/evidence/sae08-static-vectors.json').write_text(json.dumps(evidence,indent=2)+'\n',encoding='utf8')
    print('Wrote',len(vectors),'static-model vectors.')
