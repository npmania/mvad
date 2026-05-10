/* slstatus component. Add to components[] in config.h:
 *   { run_command, "mvad status", 2 },  (interval in seconds)
 * or call mvad_status() from a dwmblocks-style block. */

#include <stdio.h>
#include <string.h>

const char *
mvad_status(const char *unused)
{
	static char buf[128];
	FILE *p = popen("mvad status", "r");
	if (!p)
		return "?";
	if (!fgets(buf, sizeof(buf), p))
		buf[0] = 0;
	pclose(p);
	buf[strcspn(buf, "\n")] = 0;
	return buf;
}
