# The end-to-end tests in groups of about the same length (one to two minutes each). CI runs the groups at the same
# time, each against its own throwaway Immich; `test/run.sh` with no argument runs them all, one after the other.
# Every Test* in test/e2e must be in exactly one group: .github/scripts/e2e_groups_test.sh checks that, so a new test
# cannot be left out of CI by accident.
E2E_GROUPS="browser core hub"
E2E_GROUP_browser='^(TestBrowser|TestDropBoxGuests|TestDropBoxGuestPage)$'
E2E_GROUP_core='^(TestWhatIsCarriedOver|TestEndToEnd|TestAlbum|TestRoutes|TestHubServices|TestInvitations|TestAdmin|TestLiveEvents|TestDropBox|TestDropBoxTools)$'
E2E_GROUP_hub='^(TestAlbumWatch|TestSelfService|TestLinkHealth|TestCopyMode|TestBackupAndRestore|TestPauseAndLimits|TestTakeBackAndExpiry|TestReceiverDeleteSwitch)$'
