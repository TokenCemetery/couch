package e2e

import "strconv"

// knownFailure is the expected failure of a contract operation or behavior check.
type knownFailure struct {
	Issue  int // GitHub issue tracking the work
	Reason string
}

// Link returns the issue number in the form ALLURE_ISSUE_PATTERN expects.
func (k knownFailure) Link() string { return strconv.Itoa(k.Issue) }

var (
	identifyInputs  = knownFailure{115, "refused on purpose: the contract run has no provider image URL or provider IDs to identify with"}
	readOnlyItems   = knownFailure{115, "refused on purpose: the library is read-only, so Couch saves no item metadata or tags"}
	refusedLibrary  = knownFailure{112, "implemented; the generated request names no library, folder or task Couch has, or a folder outside the media roots, which Couch refuses"}
	parentPathText  = knownFailure{112, "implemented as text/plain, which Emby Web reads as text; the contract says JSON"}
	scanNotStopped  = knownFailure{112, "implemented; Couch cannot stop a running library scan and says so"}
	refusedAdmin    = knownFailure{113, "implemented; the generated request names no log, device or setting Couch has, or asks for an API key, a restart or a shutdown, which Couch refuses"}
	refusedUserEdit = knownFailure{111, "implemented; the generated request deletes the caller, limits libraries (#116), has no name or no target users, which Couch refuses"}
	outOfScope      = knownFailure{153, "left out of Couch on purpose; the issue gives the reason per area"}
	forgotPassword  = knownFailure{145, "Emby Web's Forgot Password flow is not implemented"}
	trackReset      = knownFailure{149, "resetting remembered audio and subtitle choices is not implemented"}
	parentalRatings = knownFailure{150, "parental ratings and rating limits are not implemented"}
	taskTriggers    = knownFailure{151, "scheduled task triggers cannot be saved"}
	offlineSync     = knownFailure{152, "offline sync and downloads are not implemented"}
	identifyOthers  = knownFailure{139, "Identify is not implemented for albums, artists, collections and people"}
	remoteImages    = knownFailure{140, "searching for and downloading images from online providers is not implemented"}
	embyBackup      = knownFailure{155, "Emby's backup API is not served; -backup is"}
	codecParameters = knownFailure{156, "codec parameters cannot be read or saved"}
	apiDocument     = knownFailure{157, "no OpenAPI or Swagger document is served"}
	releaseNotes    = knownFailure{158, "release notes are not served"}
	toneMapOptions  = knownFailure{159, "tone-mapping options cannot be read or saved"}
	userRoutes      = knownFailure{160, "typed settings, item access, user prefixes and authenticate by ID are not served"}
	webApp          = knownFailure{161, "configuration pages and web strings are not served"}
	unranked        = knownFailure{34, "not implemented; not ranked by real usage yet"}
	notCalledByWeb  = knownFailure{34, "Emby Web 4.10.0.40 does not call it; use by other clients is not ranked yet"}
	notOfferedByWeb = knownFailure{34, "Emby Web 4.10.0.40 offers it only for item flags, user policy flags or media types Couch does not send; not ranked yet"}
	emptyPartial    = knownFailure{129, "implemented; the generated request has no body, which Emby 4.10.0.40 answers with 500 and Couch refuses with 400"}
	onePrimaryImage = knownFailure{134, "Couch keeps one uploaded Primary image for a collection or playlist: it neither reorders images nor downloads one from a URL"}
)

// knownFailures lists contract operations (by operationId) and behavior
// checks (by key) that are expected to fail until implemented. To work on
// one, delete its entry, watch the test fail, then make it pass.
//
// Grouped by Emby service; generated from the first run and edited by hand.
var knownFailures = map[string]knownFailure{
	// Behavior checks
	// ArtistsService
	"getArtistsByName": notCalledByWeb,
	// BackupApi
	"getBackuprestoreBackupinfo":   embyBackup,
	"postBackuprestoreRestore":     embyBackup,
	"postBackuprestoreRestoredata": embyBackup,
	// BifService
	"getVideosByIdIndexBif": notCalledByWeb,
	// CodecParameterService
	"getEncodingCodecparameters":  codecParameters,
	"postEncodingCodecparameters": codecParameters,
	// CollectionService
	"getCollectionsByIdProvideritems": notCalledByWeb,
	// ConfigurationService
	"postSystemConfigurationByKey":   refusedAdmin,
	"postSystemConfigurationPartial": emptyPartial,
	// ConnectService
	"deleteUsersByIdConnectLink":     outOfScope,
	"getConnectExchange":             outOfScope,
	"getConnectPending":              outOfScope,
	"postUsersByIdConnectLink":       outOfScope,
	"postUsersByIdConnectLinkDelete": outOfScope,
	// DeviceService
	"getDevicesCamerauploads":  outOfScope,
	"getDevicesInfo":           refusedAdmin,
	"postDevicesCamerauploads": outOfScope,
	// DisplayPreferencesService
	"getDisplaypreferencesById":                    notOfferedByWeb,
	"postDisplaypreferencesByDisplaypreferencesid": notOfferedByWeb,
	// DynamicHlsService
	"getAudioByIdHls1ByPlaylistidBySegmentidBySegmentcontainer": notOfferedByWeb,
	"getAudioByIdLiveM3u8":                                       notOfferedByWeb,
	"getAudioByIdMainM3u8":                                       notOfferedByWeb,
	"getAudioByIdMasterM3u8":                                     notOfferedByWeb,
	"getVideosByIdLiveM3u8":                                      notCalledByWeb,
	"getVideosByIdLiveSubtitlesM3u8":                             notCalledByWeb,
	"headAudioByIdHls1ByPlaylistidBySegmentidBySegmentcontainer": notOfferedByWeb,
	"headAudioByIdMasterM3u8":                                    notOfferedByWeb,
	// EncodingInfoService
	// EnvironmentService
	"getEnvironmentDirectorycontents":  refusedLibrary,
	"getEnvironmentParentpath":         parentPathText,
	"postEnvironmentDirectorycontents": refusedLibrary,
	"postEnvironmentValidatepath":      refusedLibrary,
	// FfmpegOptionsService
	"getEncodingFfmpegoptions":  notCalledByWeb,
	"postEncodingFfmpegoptions": notCalledByWeb,
	// GameGenresService
	"getGamegenres":       notCalledByWeb,
	"getGamegenresByName": notCalledByWeb,
	// GenericUIApiService
	"getUIView":     outOfScope,
	"postUICommand": outOfScope,
	// GenresService
	"getGenresByName": notCalledByWeb,
	// HlsSegmentService
	// ImageService
	"getArtistsByNameImagesByType":           notCalledByWeb,
	"getArtistsByNameImagesByTypeByIndex":    notCalledByWeb,
	"getGamegenresByNameImagesByType":        notCalledByWeb,
	"getGamegenresByNameImagesByTypeByIndex": notCalledByWeb,
	"getGenresByNameImagesByType":            notCalledByWeb,
	"getGenresByNameImagesByTypeByIndex":     notCalledByWeb,
	"getItemsByIdImagesByTypeByIndexByTagByFormatByMaxwidthByMaxheightByPercentplayedByUnplayedcount": notCalledByWeb,
	"getMusicgenresByNameImagesByType":        notCalledByWeb,
	"getMusicgenresByNameImagesByTypeByIndex": notCalledByWeb,
	"getPersonsByNameImagesByType":            notCalledByWeb,
	"getPersonsByNameImagesByTypeByIndex":     notCalledByWeb,
	"getStudiosByNameImagesByType":            notCalledByWeb,
	"getStudiosByNameImagesByTypeByIndex":     notCalledByWeb,
	"headArtistsByNameImagesByType":           notCalledByWeb,
	"headArtistsByNameImagesByTypeByIndex":    notCalledByWeb,
	"headGamegenresByNameImagesByType":        notCalledByWeb,
	"headGamegenresByNameImagesByTypeByIndex": notCalledByWeb,
	"headGenresByNameImagesByType":            notCalledByWeb,
	"headGenresByNameImagesByTypeByIndex":     notCalledByWeb,
	"headItemsByIdImagesByTypeByIndexByTagByFormatByMaxwidthByMaxheightByPercentplayedByUnplayedcount": notCalledByWeb,
	"headMusicgenresByNameImagesByType":        notCalledByWeb,
	"headMusicgenresByNameImagesByTypeByIndex": notCalledByWeb,
	"headPersonsByNameImagesByType":            notCalledByWeb,
	"headPersonsByNameImagesByTypeByIndex":     notCalledByWeb,
	"headStudiosByNameImagesByType":            notCalledByWeb,
	"headStudiosByNameImagesByTypeByIndex":     notCalledByWeb,
	"postItemsByIdImagesByTypeByIndexIndex":    onePrimaryImage,
	"postItemsByIdImagesByTypeByIndexUrl":      onePrimaryImage,
	// InstantMixService
	"getAudiobooksNextup": notCalledByWeb,
	// ItemLookupService
	"getItemsRemotesearchImage":        identifyInputs,
	"postItemsRemotesearchApplyById":   identifyInputs,
	"postItemsRemotesearchBook":        outOfScope,
	"postItemsRemotesearchBoxset":      identifyOthers,
	"postItemsRemotesearchGame":        outOfScope,
	"postItemsRemotesearchMusicalbum":  identifyOthers,
	"postItemsRemotesearchMusicartist": identifyOthers,
	"postItemsRemotesearchMusicvideo":  outOfScope,
	"postItemsRemotesearchPerson":      identifyOthers,
	"postItemsRemotesearchTrailer":     outOfScope,
	// ItemRefreshService
	// ItemUpdateService
	"postItemsByItemid": readOnlyItems,
	// LibraryService
	"deleteItems":               outOfScope,
	"getAlbumsByIdSimilar":      notCalledByWeb,
	"getArtistsByIdSimilar":     notCalledByWeb,
	"getGamesByIdSimilar":       notCalledByWeb,
	"getItemsByIdAncestors":     notCalledByWeb,
	"getItemsByIdCriticreviews": notCalledByWeb,
	"getItemsByIdFile":          notOfferedByWeb,
	"getItemsByIdThemesongs":    notCalledByWeb,
	"getItemsByIdThemevideos":   notCalledByWeb,
	"getItemsCounts":            notCalledByWeb,
	"getItemsIntros":            notCalledByWeb,
	"getLibraryPhysicalpaths":   notCalledByWeb,
	"getMoviesByIdSimilar":      notCalledByWeb,
	"getShowsByIdSimilar":       notCalledByWeb,
	"getTrailersByIdSimilar":    notCalledByWeb,
	"postItemsByIdDelete":       outOfScope,
	"postLibraryMediaUpdated":   notCalledByWeb,
	"postLibraryMoviesAdded":    notCalledByWeb,
	"postLibraryMoviesUpdated":  notCalledByWeb,
	"postLibraryRefresh":        notCalledByWeb,
	"postLibrarySeriesAdded":    notCalledByWeb,
	"postLibrarySeriesUpdated":  notCalledByWeb,
	// LibraryStructureService
	"deleteLibraryVirtualfolders":             refusedLibrary,
	"deleteLibraryVirtualfoldersPaths":        refusedLibrary,
	"postLibraryVirtualfolders":               refusedLibrary,
	"postLibraryVirtualfoldersDelete":         refusedLibrary,
	"postLibraryVirtualfoldersLibraryoptions": refusedLibrary,
	"postLibraryVirtualfoldersName":           refusedLibrary,
	"postLibraryVirtualfoldersPaths":          refusedLibrary,
	"postLibraryVirtualfoldersPathsDelete":    refusedLibrary,
	"postLibraryVirtualfoldersPathsUpdate":    refusedLibrary,
	// LiveStreamService
	"getLivetvLiverecordingsByIdHlsBySegment":       outOfScope,
	"getLivetvLiverecordingsByIdHlsLiveM3u8":        outOfScope,
	"getLivetvLiverecordingsByIdHlsMasterM3u8":      outOfScope,
	"getLivetvLiverecordingsByIdStream":             outOfScope,
	"getLivetvLivestreamfilesByIdHlsBySegment":      outOfScope,
	"getLivetvLivestreamfilesByIdHlsLiveM3u8":       outOfScope,
	"getLivetvLivestreamfilesByIdHlsMasterM3u8":     outOfScope,
	"getLivetvLivestreamfilesByIdStreamByContainer": outOfScope,
	"headLivetvLiverecordingsByIdHlsBySegment":      outOfScope,
	"headLivetvLiverecordingsByIdHlsLiveM3u8":       outOfScope,
	"headLivetvLiverecordingsByIdHlsMasterM3u8":     outOfScope,
	"headLivetvLivestreamfilesByIdHlsBySegment":     outOfScope,
	"headLivetvLivestreamfilesByIdHlsLiveM3u8":      outOfScope,
	"headLivetvLivestreamfilesByIdHlsMasterM3u8":    outOfScope,
	// LiveTvService
	"deleteLivetvChannelmappingoptions":     outOfScope,
	"deleteLivetvChannelmappings":           outOfScope,
	"deleteLivetvListingproviders":          outOfScope,
	"deleteLivetvRecordingsById":            outOfScope,
	"deleteLivetvSeriestimersById":          outOfScope,
	"deleteLivetvTimersById":                outOfScope,
	"deleteLivetvTunerhosts":                outOfScope,
	"getLivetvAvailablerecordingoptions":    outOfScope,
	"getLivetvChannelmappingoptions":        outOfScope,
	"getLivetvChannelmappings":              outOfScope,
	"getLivetvChannels":                     outOfScope,
	"getLivetvChannelsById":                 outOfScope,
	"getLivetvChanneltags":                  outOfScope,
	"getLivetvChanneltagsPrefixes":          outOfScope,
	"getLivetvEPG":                          outOfScope,
	"getLivetvFolder":                       outOfScope,
	"getLivetvGuideinfo":                    outOfScope,
	"getLivetvInfo":                         outOfScope,
	"getLivetvListingprovidersAvailable":    outOfScope,
	"getLivetvListingprovidersDefault":      outOfScope,
	"getLivetvListingprovidersLineups":      outOfScope,
	"getLivetvManageChannels":               outOfScope,
	"getLivetvProgramsRecommended":          outOfScope,
	"getLivetvRecordingsById":               outOfScope,
	"getLivetvRecordingsFolders":            outOfScope,
	"getLivetvRecordingsGroups":             outOfScope,
	"getLivetvRecordingsSeries":             outOfScope,
	"getLivetvSeriestimers":                 outOfScope,
	"getLivetvSeriestimersById":             outOfScope,
	"getLivetvTimers":                       outOfScope,
	"getLivetvTimersById":                   outOfScope,
	"getLivetvTimersDefaults":               outOfScope,
	"getLivetvTunerhostsDefaultByType":      outOfScope,
	"getLivetvTunerhostsTypes":              outOfScope,
	"getLivetvTunersDiscover":               outOfScope,
	"getLivetvTunersDiscvover":              outOfScope,
	"headLivetvChannelmappingoptions":       outOfScope,
	"headLivetvChannelmappings":             outOfScope,
	"postLivetvChannelmappingoptions":       outOfScope,
	"postLivetvChannelmappings":             outOfScope,
	"postLivetvListingproviders":            outOfScope,
	"postLivetvListingprovidersDelete":      outOfScope,
	"postLivetvManageChannelsByIdDisabled":  outOfScope,
	"postLivetvManageChannelsByIdSortindex": outOfScope,
	"postLivetvPrograms":                    outOfScope,
	"postLivetvRecordingsByIdDelete":        outOfScope,
	"postLivetvSeriestimers":                outOfScope,
	"postLivetvSeriestimersById":            outOfScope,
	"postLivetvSeriestimersByIdDelete":      outOfScope,
	"postLivetvTimers":                      outOfScope,
	"postLivetvTimersById":                  outOfScope,
	"postLivetvTimersByIdDelete":            outOfScope,
	"postLivetvTunerhosts":                  outOfScope,
	"postLivetvTunerhostsDelete":            outOfScope,
	"postLivetvTunersByIdReset":             outOfScope,
	"putLivetvChannelmappingoptions":        outOfScope,
	"putLivetvChannelmappings":              outOfScope,
	// LocalizationService
	"getLocalizationParentalratings": parentalRatings,
	// MediaInfoService
	"getPlaybackBitratetest":   notCalledByWeb,
	"postLivestreamsClose":     notCalledByWeb,
	"postLivestreamsMediainfo": notCalledByWeb,
	"postLivestreamsOpen":      notOfferedByWeb,
	// MoviesService
	// MusicGenresService
	"getMusicgenres":       notCalledByWeb,
	"getMusicgenresByName": notCalledByWeb,
	// NotificationsService
	"postNotificationsAdmin": outOfScope,
	// OfficialRatingService
	// OpenApiService
	"getOpenapi":     apiDocument,
	"getOpenapiJson": apiDocument,
	"getSwagger":     apiDocument,
	"getSwaggerJson": apiDocument,
	// PackageService
	"deletePackagesInstallingById":     outOfScope,
	"getPackages":                      outOfScope,
	"getPackagesByName":                outOfScope,
	"getPackagesUpdates":               outOfScope,
	"postPackagesInstalledByName":      outOfScope,
	"postPackagesInstallingByIdDelete": outOfScope,
	// PartyService
	"getParties":          notCalledByWeb,
	"getPartiesInfo":      notCalledByWeb,
	"getPartiesMessages":  notCalledByWeb,
	"postParties":         notCalledByWeb,
	"postPartiesByIdJoin": notCalledByWeb,
	"postPartiesLeave":    notCalledByWeb,
	"postPartiesMessages": notCalledByWeb,
	// PersonsService
	"getPersonsByName": notCalledByWeb,
	// PlaystateService
	"deleteUsersByUseridPlayingitemsById":       notCalledByWeb,
	"postSessionsPlayingPing":                   notCalledByWeb,
	"postUsersByUseridItemsByItemidUserdata":    notCalledByWeb,
	"postUsersByUseridPlayingitemsById":         notCalledByWeb,
	"postUsersByUseridPlayingitemsByIdDelete":   notCalledByWeb,
	"postUsersByUseridPlayingitemsByIdProgress": notCalledByWeb,
	// PluginService
	"deletePluginsById":            outOfScope,
	"getPluginsByIdConfiguration":  outOfScope,
	"getPluginsByIdThumb":          outOfScope,
	"postPluginsByIdConfiguration": outOfScope,
	"postPluginsByIdDelete":        outOfScope,
	// RemoteImageService
	"getImagesRemote":                   remoteImages,
	"getItemsByIdRemoteimages":          remoteImages,
	"postItemsByIdRemoteimagesDownload": remoteImages,
	// ScheduledTaskService
	"deleteScheduledtasksRunningById":     scanNotStopped,
	"getScheduledtasksById":               refusedLibrary,
	"postScheduledtasksByIdTriggers":      taskTriggers,
	"postScheduledtasksRunningById":       refusedLibrary,
	"postScheduledtasksRunningByIdDelete": scanNotStopped,
	// SessionsService
	"deleteAuthKeysByKey":                 outOfScope,
	"deleteSessionsByIdUsersByUserid":     unranked,
	"getSessionsPlayqueue":                unranked,
	"postAuthKeys":                        refusedAdmin,
	"postAuthKeysByKeyDelete":             outOfScope,
	"postSessionsByIdCommand":             unranked,
	"postSessionsByIdCommandByCommand":    unranked,
	"postSessionsByIdMessage":             unranked,
	"postSessionsByIdPlaying":             unranked,
	"postSessionsByIdPlayingByCommand":    unranked,
	"postSessionsByIdSystemByCommand":     unranked,
	"postSessionsByIdUsersByUserid":       unranked,
	"postSessionsByIdUsersByUseridDelete": unranked,
	"postSessionsByIdViewing":             notCalledByWeb,
	"postSessionsCapabilities":            unranked,
	// StudiosService
	"getStudiosByName": notCalledByWeb,
	// SubtitleOptionsService
	"getEncodingSubtitleoptions":  notCalledByWeb,
	"postEncodingSubtitleoptions": notCalledByWeb,
	// SubtitleService
	"deleteItemsByIdSubtitlesByIndex":                      notCalledByWeb,
	"deleteVideosByIdSubtitlesByIndex":                     notCalledByWeb,
	"getItemsByIdRemotesearchSubtitlesByLanguage":          notOfferedByWeb,
	"getProvidersSubtitlesSubtitlesById":                   notOfferedByWeb,
	"getVideosByIdByMediasourceidAttachmentsByIndexStream": notCalledByWeb,
	"postItemsByIdRemotesearchSubtitlesBySubtitleid":       notOfferedByWeb,
	"postItemsByIdSubtitlesByIndexDelete":                  notCalledByWeb,
	"postVideosByIdSubtitlesByIndexDelete":                 notOfferedByWeb,
	// SuggestionsService
	"getUsersByUseridSuggestions": outOfScope,
	// SyncService
	"deleteSyncByTargetidItems":            offlineSync,
	"deleteSyncJobitemsById":               offlineSync,
	"deleteSyncJobsById":                   offlineSync,
	"getSyncItemsReady":                    offlineSync,
	"getSyncJobitems":                      offlineSync,
	"getSyncJobitemsByIdAdditionalfiles":   offlineSync,
	"getSyncJobitemsByIdFile":              offlineSync,
	"getSyncJobsById":                      offlineSync,
	"getSyncOptions":                       offlineSync,
	"getSyncTargets":                       offlineSync,
	"headSyncJobitemsByIdFile":             offlineSync,
	"postSyncByItemidStatus":               offlineSync,
	"postSyncByTargetidItemsDelete":        offlineSync,
	"postSyncData":                         offlineSync,
	"postSyncItemsCancel":                  offlineSync,
	"postSyncJobitemsByIdDelete":           offlineSync,
	"postSyncJobitemsByIdEnable":           offlineSync,
	"postSyncJobitemsByIdMarkforremoval":   offlineSync,
	"postSyncJobitemsByIdTransferred":      offlineSync,
	"postSyncJobitemsByIdUnmarkforremoval": offlineSync,
	"postSyncJobs":                         offlineSync,
	"postSyncJobsById":                     offlineSync,
	"postSyncJobsByIdDelete":               offlineSync,
	"postSyncOfflineactions":               offlineSync,
	// SystemService
	"getSystemLogsByName":      refusedAdmin,
	"getSystemLogsByNameLines": refusedAdmin,
	"getSystemReleasenotes":    releaseNotes,
	"postSystemRestart":        refusedAdmin,
	"postSystemShutdown":       refusedAdmin,
	// TagService
	"getArtistsPrefixes":      notCalledByWeb,
	"postItemsByIdTagsAdd":    readOnlyItems,
	"postItemsByIdTagsDelete": readOnlyItems,
	// ToneMapOptionsService
	"getEncodingFulltonemapoptions":    toneMapOptions,
	"getEncodingPublictonemapoptions":  toneMapOptions,
	"postEncodingFulltonemapoptions":   toneMapOptions,
	"postEncodingPublictonemapoptions": toneMapOptions,
	// TrailersService
	"getTrailers": outOfScope,
	// UniversalAudioService
	"getAudioByIdUniversalByContainer":  notCalledByWeb,
	"headAudioByIdUniversalByContainer": notCalledByWeb,
	// UserLibraryService
	"deleteUsersByUseridItemsByIdRating":     notCalledByWeb,
	"getLivetvProgramsById":                  notCalledByWeb,
	"getVideosByIdAdditionalparts":           notOfferedByWeb,
	"postItemsAccess":                        notOfferedByWeb,
	"postItemsByIdMakeprivate":               notOfferedByWeb,
	"postItemsByIdMakepublic":                notOfferedByWeb,
	"postItemsSharedLeave":                   notOfferedByWeb,
	"postUsersByUseridItemsByIdRating":       notCalledByWeb,
	"postUsersByUseridItemsByIdRatingDelete": notCalledByWeb,
	// UserNotificationsService
	"getNotificationsServicesDefaults": outOfScope,
	"postNotificationsServicesTest":    outOfScope,
	// UserService
	"deleteUsersById":                               refusedUserEdit,
	"deleteUsersByIdRecentlysearched":               userRoutes,
	"deleteUsersByIdTrackselectionsByTracktype":     trackReset,
	"getUsersByUseridTypedsettingsByKey":            userRoutes,
	"getUsersItemaccess":                            userRoutes,
	"getUsersPrefixes":                              userRoutes,
	"postUsersByIdAuthenticate":                     userRoutes,
	"postUsersByIdDelete":                           refusedUserEdit,
	"postUsersByIdPolicy":                           refusedUserEdit,
	"postUsersByIdTrackselectionsByTracktypeDelete": trackReset,
	"postUsersByUseridCopydata":                     refusedUserEdit,
	"postUsersByUseridTypedsettingsByKey":           userRoutes,
	"postUsersForgotpassword":                       forgotPassword,
	"postUsersForgotpasswordPin":                    forgotPassword,
	"postUsersNew":                                  refusedUserEdit,
	// VideoHlsService
	"getAudioByIdHlsByPlaylistidBySegmentidBySegmentcontainer":  notOfferedByWeb,
	"getVideosByIdHlsByPlaylistidBySegmentidBySegmentcontainer": notCalledByWeb,
	// VideosService
	"deleteVideosByIdAlternatesources":     outOfScope,
	"postVideosByIdAlternatesourcesDelete": outOfScope,
	"postVideosMergeversions":              outOfScope,
	// WebAppService
	"getWebConfigurationpage": webApp,
	"getWebStrings":           webApp,
	"getWebStringset":         webApp,
}
