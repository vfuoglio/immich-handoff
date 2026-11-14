# The end-to-end tests in groups of about the same length (one to two minutes each). CI runs the groups at the same
# time, each against its own throwaway Immich; `test/run.sh` with no argument runs them all, one after the other.
# Every Test* in test/e2e must be in exactly one group: .github/scripts/e2e_groups_test.sh checks that, so a new test
# cannot be left out of CI by accident.
E2E_GROUPS="browser core hub bridge"
E2E_GROUP_browser='^(TestBrowser|TestDropBoxGuests|TestDropBoxGuestPage|TestDropBoxExtras|TestDropBoxWallBestOfAndHandingOn|TestFrame|TestDropBoxWholePagePassword|TestEvent|TestSend|TestRoom)$'
E2E_GROUP_core='^(TestWhatIsCarriedOver|TestEndToEnd|TestAlbum|TestRoutes|TestHubServices|TestInvitations|TestAdmin|TestLiveEvents|TestDropBox|TestDropBoxTools|TestEventPool)$'
E2E_GROUP_hub='^(TestAlbumWatch|TestSelfService|TestLinkHealth|TestCopyMode|TestBackupAndRestore|TestPauseAndLimits|TestTakeBackAndExpiry|TestReceiverDeleteSwitch|TestMailToPeople|TestPushDevices|TestWeeklyDigest|TestPhotosByEmail)$'
# starts a second Immich of its own (the receiver's server) next to the one every group has, so it has a group to itself
E2E_GROUP_bridge='^(TestBridge)$'
