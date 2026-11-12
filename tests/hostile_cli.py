import os, pathlib, subprocess, sys, json
BIN=sys.argv[1]
ROOT=pathlib.Path(sys.argv[2]); HOME=ROOT/'ws'
def call(args, home=None, env=None):
    p=subprocess.run([BIN,*args,'--home',str(home or HOME)],capture_output=True,text=True,env={**os.environ,**(env or {})})
    return p.returncode,p.stdout,p.stderr
HOME.mkdir(parents=True)
code,_,err=call(['init']); assert code==0,err
code,_,err=call(['user','add','admin','--role','admin'],env={'REPAIRFLOW_NEW_PASSWORD':'password12345'}); assert code==0,err
code,_,err=call(['customer','add','--name','Hostile Customer']); assert code==0,err
code,_,err=call(['ticket','add','--customer','1','--device','Test Device','--issue','Test issue']); assert code==0,err
cases=[
 ('misspelled command',['ticket','lsit'],2,'usage'),
 ('unknown flag',['ticket','list','--unknown'],2,'usage'),
 ('missing ticket id',['ticket','show'],2,'usage'),
 ('non-numeric id',['ticket','show','abc'],2,'validation'),
 ('negative id',['ticket','show','-1'],2,'validation'),
 ('empty id',['ticket','show',''],2,'validation'),
 ('missing name',['customer','add'],2,'validation'),
 ('duplicate flags',['customer','add','--name','A','--name','B'],2,'usage'),
 ('extra positional',['customer','list','extra'],2,'usage'),
 ('unknown global flag',['--not-a-real-option'],2,'usage'),
 ('bad stage',['ticket','stage','1','bogus'],2,'validation'),
 ('negative price',['ticket','part-add','1','--description','x','--qty','1','--price','-1'],2,'usage'),
 ('fractional cents',['ticket','part-add','1','--description','x','--qty','1','--price','1.234'],2,'validation'),
 ('zero quantity',['ticket','part-add','1','--description','x','--qty','0','--price','1'],2,'validation'),
 ('bad quantity',['ticket','part-add','1','--description','x','--qty','abc','--price','1'],2,'validation'),
 ('bad discount',['estimate','create','1','--discount-percent','101'],2,'validation'),
 ('missing attachment',['attachment','add','--ticket','1','--file','/definitely/missing'],2,'validation'),
 ('missing estimate',['estimate','show','999'],4,'not_found'),
 ('password without env',['user','passwd','nobody'],2,'usage'),
]
results=[]
for name,args,want_code,want_text in cases:
    code,out,err=call(args)
    if code!=want_code or want_text not in (out+err):
        raise SystemExit(f'FAIL {name}: code={code} expected={want_code} text={want_text} out={out} err={err}')
    results.append((name,code))
# Text inputs are data, not shell/SQL instructions.
for i,text in enumerate(['O\'Brien "quoted"','δοκιμή','🙂 repair','line1\nline2','semi; DROP TABLE customers;--','  padded  ','back\\slash','tab\tvalue']):
    code,out,err=call(['customer','add','--name',text,'--notes',text])
    if code!=0:
        raise SystemExit(f'FAIL text {i}: code={code} err={err}')
# Filenames are data too.
for name in ['-leading.bin','unicode-δοκιμή.bin','spaces and quotes.bin']:
    p=ROOT/name; p.write_bytes(b'\x7fELF\x00\x01')
    code,out,err=call(['attachment','add','--ticket','1','--file',str(p)])
    if code!=0:
        raise SystemExit(f'FAIL filename {name}: {err}')
print('PASS:',len(results),'negative cases plus text/filename handling')
