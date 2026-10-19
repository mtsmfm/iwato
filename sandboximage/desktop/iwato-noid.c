/* LD_PRELOAD shim for the desktop: setuid(getuid()) and setgid(getgid())
 * succeed without changing anything.
 *
 * Why: the X server runs xkbcomp through popen-like code (os/utils.c Popen)
 * whose child first calls setgid(getgid()) and setuid(getuid()) and does
 * _exit(127) if either fails. OpenShell's seccomp filter makes both syscalls
 * fail with EPERM even when the id does not change, so xkbcomp never starts
 * and Xvfb dies with "XKB: Failed to compile keymap". Setting an id the
 * process already has is a no-op, so answering 0 is what the kernel would
 * have said without the filter; any other id goes to the real function.
 *
 * No headers on purpose: it is built in the image with one gcc command (see
 * the Dockerfile) and the declarations below are all it needs. */
extern unsigned int getuid(void), geteuid(void), getgid(void), getegid(void);
extern void *dlsym(void *, const char *);
#define RTLD_NEXT ((void *)-1L)

int setuid(unsigned int id) {
	static int (*real)(unsigned int);
	if (id == getuid() && id == geteuid())
		return 0;
	if (!real)
		real = (int (*)(unsigned int))dlsym(RTLD_NEXT, "setuid");
	return real(id);
}

int setgid(unsigned int id) {
	static int (*real)(unsigned int);
	if (id == getgid() && id == getegid())
		return 0;
	if (!real)
		real = (int (*)(unsigned int))dlsym(RTLD_NEXT, "setgid");
	return real(id);
}
