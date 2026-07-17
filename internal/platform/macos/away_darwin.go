//go:build darwin && cgo

package macos

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>

extern CFDictionaryRef CGSessionCopyCurrentDictionary(void);

static int larky_display_asleep(void) {
    CGDirectDisplayID displays[32];
    uint32_t count = 0;
    if (CGGetOnlineDisplayList(32, displays, &count) != kCGErrorSuccess || count == 0) {
        return 1;
    }
    for (uint32_t i = 0; i < count; i++) {
        if (!CGDisplayIsAsleep(displays[i])) {
            return 0;
        }
    }
    return 1;
}

static int larky_screen_locked(void) {
    CFDictionaryRef session = CGSessionCopyCurrentDictionary();
    if (session == NULL) {
        return 0;
    }
    CFBooleanRef value = (CFBooleanRef)CFDictionaryGetValue(session, CFSTR("CGSSessionScreenIsLocked"));
    int locked = value != NULL && CFGetTypeID(value) == CFBooleanGetTypeID() && CFBooleanGetValue(value);
    CFRelease(session);
    return locked;
}
*/
import "C"

func detectSystemState() (State, error) {
	displayAsleep := C.larky_display_asleep() != 0
	screenLocked := C.larky_screen_locked() != 0
	return State{
		DisplayAsleep: displayAsleep,
		ScreenLocked:  screenLocked,
		Away:          displayAsleep || screenLocked,
		Method:        "coregraphics",
	}, nil
}
