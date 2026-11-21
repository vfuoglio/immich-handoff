# The end-to-end tests in groups of about the same length (one to two minutes each). CI runs the groups at the same
# time, each against its own throwaway Immich; `test/run.sh` with no argument runs them all, one after the other.
# Every Test* in test/e2e must be in exactly one group: .github/scripts/e2e_groups_test.sh checks that, so a new test
# cannot be left out of CI by accident.
E2E_GROUPS="browser core core2 hub hub2 hub3 bridge"
E2E_GROUP_browser='^(TestBrowser|TestDropBoxGuests|TestDropBoxGuestPage|TestDropBoxExtras|TestDropBoxWallBestOfAndHandingOn|TestDropBoxWholePagePassword|TestEvent|TestRoom|TestRoomRounds|TestAlbumPickers|TestPairingPages)$'
E2E_GROUP_core='^(TestWhatIsCarriedOver|TestEndToEnd|TestAlbum|TestRoutes|TestHubServices|TestInvitations|TestAdmin|TestLiveEvents|TestDropBox|TestFolderAlbumsSpike|TestStackingSpike)$'
# the person-facing features, split in three of about two minutes each (hub, hub2, hub3)
E2E_GROUP_hub='^(TestRules|TestPairing|TestWeeklyDigest|TestStacking|TestLinkHealth|TestRulesImmichRules)$'
E2E_GROUP_hub2='^(TestSmartAlbums|TestDestinations|TestAlbumWatch|TestPauseAndLimits|TestCopyMode|TestFolderAlbums|TestBackupAndRestore|TestAddToMyLibrary|TestPushDevices)$'
E2E_GROUP_hub3='^(TestAlbumTree|TestCircles|TestOnThisDay|TestHandOverButton|TestSelfService|TestTakeBackAndExpiry|TestReceiverDeleteSwitch|TestImmichMarkup|TestMailToPeople|TestPhotosByEmail)$'
# the slowest of what was in core and browser, in a group of its own so no group is much longer than two minutes of tests
E2E_GROUP_core2='^(TestEventPool|TestFollowSharedAlbum|TestDropBoxTools|TestSend|TestFrame)$'
# starts a second Immich of its own (the receiver's server) next to the one every group has, so it has a group to itself
E2E_GROUP_bridge='^(TestBridge|TestSharedAlbumImmichRules|TestSharedAlbumsTwoServers)$'
