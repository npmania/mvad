//go:build darwin

package main

// The ObjC lives here: tray_darwin.go uses //export, which limits its
// preamble to declarations.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit

#import <AppKit/AppKit.h>
#import <CoreServices/CoreServices.h>
#include <stdlib.h>

extern int trayClicked(int);
extern void trayReopen(void);

@interface MvadTray : NSObject
@end

@implementation MvadTray
- (void)click:(NSMenuItem *)it {
	if (trayClicked((int)it.tag))
		[NSApp activateIgnoringOtherApps:YES];
}
- (void)reopen:(NSAppleEventDescriptor *)e reply:(NSAppleEventDescriptor *)r {
	trayReopen();
	[NSApp activateIgnoringOtherApps:YES];
}
@end

static NSStatusItem *item;
static MvadTray *target;
static NSMenu *building;

static NSImage *disc(int up) {
	NSColor *c = up ?
		[NSColor colorWithSRGBRed:0x5D/255.0 green:0xBF/255.0 blue:0x8E/255.0 alpha:1] :
		[NSColor colorWithSRGBRed:0xC0/255.0 green:0x20/255.0 blue:0x20/255.0 alpha:1];
	return [NSImage imageWithSize:NSMakeSize(18, 18) flipped:NO drawingHandler:^BOOL(NSRect r) {
		[c setFill];
		[[NSBezierPath bezierPathWithOvalInRect:NSInsetRect(r, 3, 3)] fill];
		return YES;
	}];
}

void trayStart(void) {
	// Gio's applicationDidFinishLaunching sets the Regular policy, which
	// adds a Dock icon; the hop through the main queue lands after it.
	[[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationDidFinishLaunchingNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
		dispatch_async(dispatch_get_main_queue(), ^{
			[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
			[[NSAppleEventManager sharedAppleEventManager] setEventHandler:target andSelector:@selector(reopen:reply:) forEventClass:kCoreEventClass andEventID:kAEReopenApplication];
		});
	}];
	// Gio cascades every new window down and right of the last one.
	[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowDidBecomeKeyNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
		static __weak NSWindow *seen;
		if (n.object != seen) {
			seen = n.object;
			[seen center];
		}
	}];
	dispatch_async(dispatch_get_main_queue(), ^{
		target = [MvadTray new];
		item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
	});
}

void traySet(int up, char *tip) {
	NSString *s = [NSString stringWithUTF8String:tip];
	free(tip);
	dispatch_async(dispatch_get_main_queue(), ^{
		item.button.image = disc(up);
		item.button.toolTip = s;
	});
}

// trayAdd appends to the menu being built; a child goes into the
// submenu of the last item.
void trayAdd(int tag, int child, char *label) {
	NSString *s = label ? [NSString stringWithUTF8String:label] : nil;
	free(label);
	dispatch_async(dispatch_get_main_queue(), ^{
		if (building == nil)
			building = [NSMenu new];
		NSMenu *m = building;
		if (child) {
			NSMenuItem *last = building.itemArray.lastObject;
			if (last.submenu == nil) {
				last.submenu = [NSMenu new];
				last.action = nil;
			}
			m = last.submenu;
		}
		NSMenuItem *it = [NSMenuItem separatorItem];
		if (s != nil) {
			it = [[NSMenuItem alloc] initWithTitle:s action:@selector(click:) keyEquivalent:@""];
			it.target = target;
		}
		it.tag = tag;
		[m addItem:it];
	});
}

void trayCommit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		item.menu = building;
		building = nil;
	});
}

int appDark(void) {
	NSAppearanceName n = [[NSApplication sharedApplication].effectiveAppearance
		bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
	return [n isEqualToString:NSAppearanceNameDarkAqua];
}
*/
import "C"
