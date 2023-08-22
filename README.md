# discordyt
WIP, will add install steps later

## Notes
TODO:
- do binary search in find()
- unhandlded json encode/decodes
- rename *_consts.go files
- add state handling to the start of Gateway methods
- send silences frames after song ends
- unhandled interaction resp errors
- proper error definitions (replace error.New)
- gateway properties should be from environment
- add skip command

Done:
- add runtime to "Playing song" response -> done
- Use UDP socket thread to monitor silence and leave channel -> done, but leave channel is handled outside of socket thread
- change guild state to store channel id as string instead of *string -> done
- play queue!!! -> done, but not thoroughly tested
- better way to build string buffer for response -> done
- songId is input directly into full path, should be checked -> basically done, as long as we trust youtube api output
- change consts and struct naming -> done
- handle INVALID_SESSION opcode -> mostly done
- handle non-existing GuildStates in handle dispatch -> i think my solution is ok
- unclear what happens when user manually drags bot into a different channel
    - when is voice server update event sent? -> when moved to a different channel, either by moderator or by bot itself
    - when should voice gateway be reconnected (not resumed)? -> after a pair of voice server/state update events are received with a non-nil channel
    - what session id, token, and url should voice gateway use on resume/reconnect? -> same session id, token, and url used on first connect
- add Gateway Reconnect that preserves guild states -> done
- add Gateway Close -> done
- add SIGINT handling (need to close websockets) -> done
- Voice gateways might not need to be closed if Gateway relaunches? -> done
- query youtube api to check if id is valid -> done
- better play command in general -> done

Ideas:
- Connect and Listen should be combined (Launch) -> No
- send channel leave when startVoiceGw fails?
