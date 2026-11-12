//! Notification text the shell will show.
//!
//! These strings match `needsYouNotification` in `frontend/src/shell/needsYou.ts`.
//! Anything else is rejected so a gateway page cannot put Capsule names, prompts,
//! or permission text on the lock screen.

pub const NEEDS_YOU_PHRASE: &str = "Meridian needs you";

pub fn validate_needs_you(title: &str, body: &str) -> Result<u32, &'static str> {
    if title != NEEDS_YOU_PHRASE {
        return Err("rejected");
    }
    if body == "1 needs you" {
        return Ok(1);
    }
    let Some(count) = body.strip_suffix(" need you") else {
        return Err("rejected");
    };
    if count.len() > 3 || count.starts_with('0') || !count.bytes().all(|byte| byte.is_ascii_digit())
    {
        return Err("rejected");
    }
    let value: u32 = count.parse().map_err(|_| "rejected")?;
    if (2..=999).contains(&value) {
        Ok(value)
    } else {
        Err("rejected")
    }
}

pub fn validate_attention(count: u32) -> Result<u32, &'static str> {
    if count > 999 {
        Err("rejected")
    } else {
        Ok(count)
    }
}

pub fn attention_tooltip(count: u32) -> String {
    if count == 0 {
        "Meridian".to_string()
    } else if count == 1 {
        "Meridian — 1 needs you".to_string()
    } else {
        format!("Meridian — {count} need you")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_the_content_free_payloads() {
        assert_eq!(validate_needs_you(NEEDS_YOU_PHRASE, "1 needs you"), Ok(1));
        assert_eq!(validate_needs_you(NEEDS_YOU_PHRASE, "2 need you"), Ok(2));
        assert_eq!(
            validate_needs_you(NEEDS_YOU_PHRASE, "999 need you"),
            Ok(999)
        );
    }

    #[test]
    fn rejects_names_prompts_and_lookalikes() {
        assert!(validate_needs_you("Meridian needs you: fix tests", "1 needs you").is_err());
        assert!(validate_needs_you(NEEDS_YOU_PHRASE, "1 needs you to approve bash").is_err());
        assert!(validate_needs_you(NEEDS_YOU_PHRASE, "1 need you").is_err());
        assert!(validate_needs_you(NEEDS_YOU_PHRASE, "01 need you").is_err());
        assert!(validate_needs_you(NEEDS_YOU_PHRASE, "1000 need you").is_err());
        assert!(validate_needs_you(NEEDS_YOU_PHRASE, "").is_err());
        assert!(validate_attention(1000).is_err());
        assert_eq!(validate_attention(0), Ok(0));
    }

    #[test]
    fn tooltip_has_no_room_for_a_capsule_name() {
        assert_eq!(attention_tooltip(0), "Meridian");
        assert_eq!(attention_tooltip(1), "Meridian — 1 needs you");
        assert_eq!(attention_tooltip(4), "Meridian — 4 need you");
    }
}
