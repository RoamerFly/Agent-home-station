/* Isolated differential-test harness. Only this program is compiled as source.
 * The inspected library is loaded AFTER the syscall policy is installed.
 * Run only via run_rqtx_oracle.sh in its read-only chroot and network/PID namespace.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <unistd.h>

#define DENY(nr) BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K,(nr),0,1), BPF_STMT(BPF_RET|BPF_K,SECCOMP_RET_ERRNO|EPERM)
static void policy(void) {
 struct rlimit cpu={10,10}, memory={256*1024*1024,256*1024*1024}, zero={0,0}, files={16,16};
 if(setrlimit(RLIMIT_CPU,&cpu)||setrlimit(RLIMIT_AS,&memory)||setrlimit(RLIMIT_FSIZE,&zero)||setrlimit(RLIMIT_CORE,&zero)||setrlimit(RLIMIT_NPROC,&zero)||setrlimit(RLIMIT_NOFILE,&files)) exit(80);
 struct sock_filter f[]={
  BPF_STMT(BPF_LD|BPF_W|BPF_ABS,offsetof(struct seccomp_data,arch)),
  BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K,AUDIT_ARCH_X86_64,1,0),
  BPF_STMT(BPF_RET|BPF_K,SECCOMP_RET_KILL_PROCESS),
  BPF_STMT(BPF_LD|BPF_W|BPF_ABS,offsetof(struct seccomp_data,nr)),
  DENY(__NR_socket),DENY(__NR_socketpair),DENY(__NR_connect),DENY(__NR_bind),DENY(__NR_listen),
  DENY(__NR_sendto),DENY(__NR_sendmsg),DENY(__NR_sendmmsg),DENY(__NR_execve),DENY(__NR_execveat),
  DENY(__NR_ptrace),DENY(__NR_process_vm_writev),DENY(__NR_process_vm_readv),
  DENY(__NR_clone),DENY(__NR_clone3),DENY(__NR_fork),DENY(__NR_vfork),DENY(__NR_mount),
  DENY(__NR_umount2),DENY(__NR_unshare),DENY(__NR_setns),DENY(__NR_bpf),DENY(__NR_keyctl),
  BPF_STMT(BPF_RET|BPF_K,SECCOMP_RET_ALLOW)
 };
 struct sock_fprog p={.len=(unsigned short)(sizeof(f)/sizeof(f[0])),.filter=f};
 if(prctl(PR_SET_NO_NEW_PRIVS,1,0,0,0)||prctl(PR_SET_SECCOMP,SECCOMP_MODE_FILTER,&p)) exit(81);
 if(getuid()!=65534 || geteuid()!=65534 || prctl(PR_GET_SECCOMP)!=2) exit(82);
}
int main(int argc,char **argv) {
 if(argc<2)return 2;
 for(int i=1;i<argc;i++)if(strlen(argv[i])!=32 || strspn(argv[i],"0123456789abcdef")!=32)return 3;
 policy();
 void *lib=dlopen("/libv08.so",RTLD_NOW|RTLD_LOCAL);
 if(!lib){fprintf(stderr,"dlopen failed: %s\n",dlerror());return 4;}
 uint32_t (*fn)(char *)=(uint32_t(*)(char *))dlsym(lib,"rqtx");if(!fn)return 5;
 fprintf(stderr,"oracle isolation: uid=65534 seccomp=2; only rqtx selected; per-process limits installed\n");
 for(int i=1;i<argc;i++){uint32_t out=fn(argv[i]);printf("%s\t%u\n",argv[i],out);fflush(stdout);}
 return 0;
}
