# discordyt
WIP, will add install steps later

## Notes
TODO:
- unhandlded json encode/decodes
- change consts and struct naming
- rename *_consts.go files
- add state handling to the start of connect, listen, reconnect, playaudio, join channel
- Use UDP socket thread to monitor silence and leave channel
- send silences frames after song ends
- unhandled interaction resp errors
- better way to build string buffer for response
- play queue!!!
- proper error definitions (replace error.New)
- songId is input directly into full path, should be checked
- gateway properties should be from environment

Done:
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
 
Ideas:
- query youtube api to check if id is valid
- better play command in general
- Connect and Listen should be combined (Launch) -> No
- send channel leave when startVoiceGw fails?
