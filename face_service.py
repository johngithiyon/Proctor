from flask import Flask, request, jsonify
import cv2
import numpy as np
import base64
import os
import time
import mediapipe as mp
from ultralytics import YOLO
from collections import defaultdict
import logging

app = Flask(__name__)

# Set up logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# --- VIOLATION TRACKING ---
# Store violation counts for each user. In a real app, use a database.
violation_counts = defaultdict(int)
MAX_VIOLATIONS = 10  # Increased to match the Go backend

# --- MediaPipe Setup ---
mp_face_mesh = mp.solutions.face_mesh
# Using refined landmarks around the eyes for better accuracy
# Corrected eye landmark indices
LEFT_EYE_INDICES = [362, 382, 381, 380, 374, 373, 390, 249, 263, 466, 388, 387, 386, 385, 384, 398]
RIGHT_EYE_INDICES = [33, 7, 163, 144, 145, 153, 154, 155, 133, 173, 157, 158, 159, 160, 161, 246]
# Iris center landmarks
LEFT_IRIS_CENTER = 473
RIGHT_IRIS_CENTER = 468

# --- Gaze Detection Thresholds ---
HORIZONTAL_THRESHOLD = 0.35  # Adjusted threshold for better accuracy

# --- Object Detection Setup ---
# Download YOLO model if it doesn't exist
try:
    model = YOLO('yolov8n.pt')
    logger.info("YOLO model loaded successfully")
except Exception as e:
    logger.error(f"Error loading YOLO model: {e}")
    model = None

# Define prohibited items (COCO dataset class names)
PROHIBITED_ITEMS = {
    'cell phone': 'MOBILE_PHONE',
    'book': 'BOOK',
    'laptop': 'LAPTOP',
    'mouse': 'MOUSE',
    'keyboard': 'KEYBOARD',
    'remote': 'REMOTE',
    'cup': 'CUP'  # Sometimes airpods cases might be detected as cups
}

# --- Existing Setup ---
os.makedirs("captured_images", exist_ok=True)

def save_image_from_base64(data_url):
    parts = data_url.split(",")
    if len(parts) != 2:
        return None
    decoded = base64.b64decode(parts[1])
    timestamp = time.strftime("%Y%m%d_%H%M%S")
    filename = f"captured_images/{timestamp}.png"
    with open(filename, "wb") as f:
        f.write(decoded)
    return filename

def compare_faces(ref_path, curr_path):
    """
    Compare two faces using template matching and face landmarks
    Returns True if faces match, otherwise False
    """
    ref_img = cv2.imread(ref_path)
    curr_img = cv2.imread(curr_path)
    
    if ref_img is None or curr_img is None:
        return False

    # Convert to grayscale
    ref_gray = cv2.cvtColor(ref_img, cv2.COLOR_BGR2GRAY)
    curr_gray = cv2.cvtColor(curr_img, cv2.COLOR_BGR2GRAY)

    # Use template matching as a first check
    res = cv2.matchTemplate(curr_gray, ref_gray, cv2.TM_CCOEFF_NORMED)
    _, max_val, _, _ = cv2.minMaxLoc(res)
    
    # If template matching score is high enough, consider it a match
    if max_val >= 0.7:
        return True
    
    # If template matching is inconclusive, use face landmarks for more accurate comparison
    with mp_face_mesh.FaceMesh(
        max_num_faces=1,
        refine_landmarks=True,
        min_detection_confidence=0.5,
        min_tracking_confidence=0.5
    ) as face_mesh:
        # Get landmarks for both images
        ref_rgb = cv2.cvtColor(ref_img, cv2.COLOR_BGR2RGB)
        curr_rgb = cv2.cvtColor(curr_img, cv2.COLOR_BGR2RGB)
        
        ref_results = face_mesh.process(ref_rgb)
        curr_results = face_mesh.process(curr_rgb)
        
        if ref_results.multi_face_landmarks and curr_results.multi_face_landmarks:
            ref_landmarks = ref_results.multi_face_landmarks[0].landmark
            curr_landmarks = curr_results.multi_face_landmarks[0].landmark
            
            # Calculate the distance between corresponding landmarks
            total_distance = 0
            count = 0
            
            # Compare key facial features (eyes, nose, mouth)
            key_indices = [1, 33, 263, 61, 291, 13, 14]  # Nose tip, eye corners, mouth corners
            
            for idx in key_indices:
                if idx < len(ref_landmarks) and idx < len(curr_landmarks):
                    ref_point = ref_landmarks[idx]
                    curr_point = curr_landmarks[idx]
                    
                    distance = np.sqrt((ref_point.x - curr_point.x)**2 + (ref_point.y - curr_point.y)**2)
                    total_distance += distance
                    count += 1
            
            if count > 0:
                avg_distance = total_distance / count
                # If average distance is small enough, consider it a match
                return avg_distance < 0.1  # Threshold for landmark comparison
    
    return False

def get_gaze_ratio(landmarks, eye_indices, iris_index):
    try:
        # Get eye corner coordinates
        eye_left_corner = landmarks[eye_indices[0]]
        eye_right_corner = landmarks[eye_indices[1]]
        # Get iris center coordinates
        iris = landmarks[iris_index]

        # Calculate horizontal gaze ratio
        eye_width = eye_right_corner.x - eye_left_corner.x
        if eye_width == 0:
            return 0.5  # Avoid division by zero, assume center
        
        gaze_ratio = (iris.x - eye_left_corner.x) / eye_width
        return gaze_ratio
    except (IndexError, AttributeError) as e:
        logger.error(f"Error in get_gaze_ratio: {e}")
        return 0.5  # Return center if landmarks are not found

def detect_gaze(image):
    with mp_face_mesh.FaceMesh(
        max_num_faces=1,
        refine_landmarks=True,
        min_detection_confidence=0.5,
        min_tracking_confidence=0.5
    ) as face_mesh:
        # Convert the BGR image to RGB
        rgb_image = cv2.cvtColor(image, cv2.COLOR_BGR2RGB)
        results = face_mesh.process(rgb_image)

        if results.multi_face_landmarks:
            landmarks = results.multi_face_landmarks[0].landmark
            
            # Use correct indices for eye corners
            left_eye_corner_indices = [33, 133]  # Left eye corners
            right_eye_corner_indices = [362, 263]  # Right eye corners
            
            # Calculate gaze ratio for both eyes
            left_gaze_ratio = get_gaze_ratio(landmarks, left_eye_corner_indices, LEFT_IRIS_CENTER)
            right_gaze_ratio = get_gaze_ratio(landmarks, right_eye_corner_indices, RIGHT_IRIS_CENTER)
            
            # Average the gaze ratio for more stability
            avg_gaze_ratio = (left_gaze_ratio + right_gaze_ratio) / 2
            
            logger.info(f"Gaze ratios - Left: {left_gaze_ratio}, Right: {right_gaze_ratio}, Average: {avg_gaze_ratio}")
            
            # Check if gaze is off-center
            if avg_gaze_ratio < HORIZONTAL_THRESHOLD or avg_gaze_ratio > (1 - HORIZONTAL_THRESHOLD):
                return False  # Gaze violation
            else:
                return True  # Gaze is OK
        return True  # No face detected, assume OK to avoid false positives

def detect_multiple_faces(image):
    """
    Detect if there are multiple faces in the image
    Returns True if multiple faces are detected, otherwise False
    """
    with mp_face_mesh.FaceMesh(
        max_num_faces=5,  # Increase max_num_faces to detect multiple faces
        refine_landmarks=True,
        min_detection_confidence=0.5,
        min_tracking_confidence=0.5
    ) as face_mesh:
        # Convert the BGR image to RGB
        rgb_image = cv2.cvtColor(image, cv2.COLOR_BGR2RGB)
        results = face_mesh.process(rgb_image)

        # Check if multiple faces are detected
        if results.multi_face_landmarks and len(results.multi_face_landmarks) > 1:
            return True
        return False

def detect_prohibited_items(image):
    """
    Detect prohibited items in the image using YOLO
    Returns the violation type if any prohibited item is found, otherwise None
    """
    if model is None:
        logger.error("YOLO model not loaded")
        return None
        
    try:
        # Run YOLO detection with confidence threshold
        results = model(image, verbose=False, conf=0.5)
        
        # Check if any prohibited items are detected
        for result in results:
            boxes = result.boxes
            for box in boxes:
                # Get class name and confidence
                cls = int(box.cls[0])
                conf = float(box.conf[0])
                class_name = model.names[cls]
                
                logger.info(f"Detected object: {class_name} with confidence: {conf}")
                
                # Check if this is a prohibited item with sufficient confidence
                if class_name.lower() in PROHIBITED_ITEMS and conf > 0.5:
                    return PROHIBITED_ITEMS[class_name.lower()]
        
        # Special detection for airpods/headsets (not in COCO dataset)
        return detect_airpods_headsets(image)
        
    except Exception as e:
        logger.error(f"Error in object detection: {e}")
        return None

def detect_airpods_headsets(image):
    """
    Improved detection for airpods/headsets using color and position
    """
    try:
        # Convert to HSV for better color detection
        hsv = cv2.cvtColor(image, cv2.COLOR_BGR2HSV)
        
        # Define range for white color (airpods are typically white)
        lower_white = np.array([0, 0, 200])
        upper_white = np.array([180, 30, 255])
        
        # Define range for black color (headsets are typically black)
        lower_black = np.array([0, 0, 0])
        upper_black = np.array([180, 255, 30])
        
        # Threshold the HSV image to get white and black colors
        white_mask = cv2.inRange(hsv, lower_white, upper_white)
        black_mask = cv2.inRange(hsv, lower_black, upper_black)
        
        # Combine masks
        mask = cv2.bitwise_or(white_mask, black_mask)
        
        # Find contours in the mask
        contours, _ = cv2.findContours(mask, cv2.RETR_TREE, cv2.CHAIN_APPROX_SIMPLE)
        
        # Get face landmarks to determine ear positions
        with mp_face_mesh.FaceMesh(
            max_num_faces=1,
            refine_landmarks=True,
            min_detection_confidence=0.5,
            min_tracking_confidence=0.5
        ) as face_mesh:
            # Convert the BGR image to RGB
            rgb_image = cv2.cvtColor(image, cv2.COLOR_BGR2RGB)
            results = face_mesh.process(rgb_image)
            
            if results.multi_face_landmarks:
                landmarks = results.multi_face_landmarks[0].landmark
                h, w, _ = image.shape
                
                # Approximate ear positions using more accurate landmarks
                left_ear = (int(landmarks[93].x * w), int(landmarks[93].y * h))
                right_ear = (int(landmarks[323].x * w), int(landmarks[323].y * h))
                
                # Check if any contours are near the ears
                for contour in contours:
                    area = cv2.contourArea(contour)
                    if 50 < area < 500:  # Filter out very small and very large contours
                        M = cv2.moments(contour)
                        if M["m00"] != 0:
                            cx = int(M["m10"] / M["m00"])
                            cy = int(M["m01"] / M["m00"])
                            
                            # Check if this object is near either ear
                            left_dist = np.sqrt((cx - left_ear[0])**2 + (cy - left_ear[1])**2)
                            right_dist = np.sqrt((cx - right_ear[0])**2 + (cy - right_ear[1])**2)
                            
                            # If the object is close to an ear, it might be an airpod or headset
                            if left_dist < 70 or right_dist < 70:
                                return "AIRPODS_OR_HEADSET"
        
        return None
    except Exception as e:
        logger.error(f"Error in airpods/headset detection: {e}")
        return None

def detect_face(image):
    """
    Detect if there is a face in the image
    Returns True if a face is detected, otherwise False
    """
    with mp_face_mesh.FaceMesh(
        max_num_faces=1,
        refine_landmarks=True,
        min_detection_confidence=0.5,
        min_tracking_confidence=0.5
    ) as face_mesh:
        # Convert the BGR image to RGB
        rgb_image = cv2.cvtColor(image, cv2.COLOR_BGR2RGB)
        results = face_mesh.process(rgb_image)

        # Check if a face is detected
        if results.multi_face_landmarks:
            return True
        return False

@app.route("/validate-face", methods=["POST"])
def validate_face():
    img_data = request.form.get("image")
    reference_face_path = request.form.get("reference_face")
    
    if not img_data:
        return "ERROR", 400

    curr_path = save_image_from_base64(img_data)
    
    # Load the image for analysis
    image = cv2.imread(curr_path)
    
    # Check if a face is detected
    if not detect_face(image):
        return "NO_FACE_DETECTED"
    
    # If reference face is provided, compare faces
    if reference_face_path and os.path.exists(reference_face_path):
        if compare_faces(reference_face_path, curr_path):
            return "FACE_MATCH"
        else:
            return "NO_FACE_MATCH"
    else:
        # Just check if a face is detected
        return "FACE_DETECTED"

@app.route("/capture", methods=["POST"])
def capture():
    img_data = request.form.get("image")
    username = request.form.get("username")
    noise_violation = request.form.get("noise_violation")
    reference_face_path = request.form.get("reference_face") # This is now sent from frontend

    if not img_data or not username:
        return "ERROR", 400

    curr_path = save_image_from_base64(img_data)
    
    # Load the image for analysis
    image = cv2.imread(curr_path)
    
    # Check for multiple faces in the first image as well
    if reference_face_path is None:
        if detect_multiple_faces(image):
            logger.info(f"Multiple faces detected for user {username}")
            return "MULTIPLE_FACES"
        return "OK"

    # 1. Check for multiple faces first
    if detect_multiple_faces(image):
        logger.info(f"Multiple faces detected for user {username}")
        return "MULTIPLE_FACES"

    # 2. Check for face mismatch with the reference face from login
    if not compare_faces(reference_face_path, curr_path):
        logger.info(f"Face mismatch for user {username}")
        return "FACE_MISMATCH"

    # 3. Check for prohibited items
    prohibited_item = detect_prohibited_items(image)
    if prohibited_item:
        logger.info(f"Prohibited item detected for user {username}: {prohibited_item}")
        violation_counts[username] += 1
        count = violation_counts[username]
        if count >= MAX_VIOLATIONS:
            return "MAX_VIOLATIONS"
        # Return in the format expected by the frontend
        return f"VIOLATION:PROHIBITED_ITEM:{prohibited_item}:{count}"

    # 4. Check for gaze violation
    if not detect_gaze(image):
        logger.info(f"Gaze violation for user {username}")
        violation_counts[username] += 1
        count = violation_counts[username]
        if count >= MAX_VIOLATIONS:
            return "MAX_VIOLATIONS"
        return f"VIOLATION:GAZE_VIOLATION:{count}"

    # 5. Then, check for noise violation
    if noise_violation == "true":
        logger.info(f"Noise violation for user {username}")
        violation_counts[username] += 1
        count = violation_counts[username]
        if count >= MAX_VIOLATIONS:
            return "MAX_VIOLATIONS"
        return f"VIOLATION:NOISE_VIOLATION:{count}"

    # If all checks pass
    logger.info(f"No violations for user {username}")
    return "OK"

# --- NEW ENDPOINTS FOR VIOLATION TRACKING ---

@app.route("/fullscreen-violation", methods=["POST"])
def fullscreen_violation():
    username = request.form.get("username")
    if not username: return "ERROR", 400
    
    violation_counts[username] += 1
    count = violation_counts[username]
    
    if count >= MAX_VIOLATIONS:
        return "MAX_VIOLATIONS"
    
    return f"VIOLATION:FULLSCREEN:{count}"

@app.route("/tab-change-violation", methods=["POST"])
def tab_change_violation():
    username = request.form.get("username")
    if not username: return "ERROR", 400
        
    violation_counts[username] += 1
    count = violation_counts[username]
    
    if count >= MAX_VIOLATIONS:
        return "MAX_VIOLATIONS"
    
    return f"VIOLATION:TAB_CHANGE:{count}"

@app.route("/window-change-violation", methods=["POST"])
def window_change_violation():
    username = request.form.get("username")
    if not username: return "ERROR", 400
        
    violation_counts[username] += 1
    count = violation_counts[username]
    
    if count >= MAX_VIOLATIONS:
        return "MAX_VIOLATIONS"
    
    return f"VIOLATION:WINDOW_CHANGE:{count}"

# --- ENDPOINT FOR SUBMITTING EXAM ---
@app.route("/submit", methods=["POST"])
def submit_exam():
    username = request.form.get("username")
    score = request.form.get("score")
    logger.info(f"User {username} submitted exam with score: {score}")
    # In a real app, you would save this to a database.
    return "OK"

if __name__ == "__main__":
    app.run(port=5000, debug=True)