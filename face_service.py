from flask import Flask, request, jsonify
import cv2
import numpy as np
import base64
import os
import time
import mediapipe as mp
from ultralytics import YOLO

app = Flask(__name__)

# --- MediaPipe Setup ---
mp_face_mesh = mp.solutions.face_mesh
# Using refined landmarks around the eyes for better accuracy
LEFT_EYE_INDICES = [33, 7, 163, 144, 145, 153, 154, 155, 133, 173, 157, 158, 159, 160, 161, 246]
RIGHT_EYE_INDICES = [362, 398, 384, 385, 386, 387, 388, 466, 263, 249, 390, 373, 374, 380, 381, 382]
# Iris center landmarks
LEFT_IRIS_CENTER = 468
RIGHT_IRIS_CENTER = 473

# --- Gaze Detection Thresholds ---
# These values might need tuning based on camera angle and distance.
# A value of 0.5 means the iris is perfectly centered horizontally.
HORIZONTAL_THRESHOLD = 0.35 # If iris is less than 35% from the left or more than 65% from the left, it's a violation.

# --- Object Detection Setup ---
# Load YOLO model
model = YOLO('yolov8n.pt')  # Using the nano version for faster processing

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
    ref_img = cv2.imread(ref_path)
    curr_img = cv2.imread(curr_path)
    if ref_img is None or curr_img is None:
        return False

    ref_gray = cv2.cvtColor(ref_img, cv2.COLOR_BGR2GRAY)
    curr_gray = cv2.cvtColor(curr_img, cv2.COLOR_BGR2GRAY)

    res = cv2.matchTemplate(curr_gray, ref_gray, cv2.TM_CCOEFF_NORMED)
    _, max_val, _, _ = cv2.minMaxLoc(res)

    return max_val >= 0.6

def get_gaze_ratio(landmarks, eye_indices, iris_index):
    try:
        # Get eye corner coordinates
        eye_left_corner = landmarks[eye_indices[0]]
        eye_right_corner = landmarks[eye_indices[8]]
        # Get iris center coordinates
        iris = landmarks[iris_index]

        # Calculate horizontal gaze ratio
        eye_width = eye_right_corner.x - eye_left_corner.x
        if eye_width == 0:
            return 0.5 # Avoid division by zero, assume center
        
        gaze_ratio = (iris.x - eye_left_corner.x) / eye_width
        return gaze_ratio
    except IndexError:
        return 0.5 # Return center if landmarks are not found

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
            
            # Calculate gaze ratio for both eyes
            left_gaze_ratio = get_gaze_ratio(landmarks, LEFT_EYE_INDICES, LEFT_IRIS_CENTER)
            right_gaze_ratio = get_gaze_ratio(landmarks, RIGHT_EYE_INDICES, RIGHT_IRIS_CENTER)
            
            # Average the gaze ratio for more stability
            avg_gaze_ratio = (left_gaze_ratio + right_gaze_ratio) / 2
            
            # Check if gaze is off-center
            if avg_gaze_ratio < HORIZONTAL_THRESHOLD or avg_gaze_ratio > (1 - HORIZONTAL_THRESHOLD):
                return False # Gaze violation
            else:
                return True # Gaze is OK
        return True # No face detected, assume OK to avoid false positives

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
    try:
        # Run YOLO detection
        results = model(image, verbose=False)
        
        # Check if any prohibited items are detected
        for result in results:
            boxes = result.boxes
            for box in boxes:
                # Get class name
                cls = int(box.cls[0])
                class_name = model.names[cls]
                
                # Check if this is a prohibited item
                if class_name.lower() in PROHIBITED_ITEMS:
                    return PROHIBITED_ITEMS[class_name.lower()]
        
        # Special detection for airpods/headsets (not in COCO dataset)
        # We'll use a simple approach to detect white objects near ears
        return detect_airpods_headsets(image)
        
    except Exception as e:
        print(f"Error in object detection: {e}")
        return None

def detect_airpods_headsets(image):
    """
    Simple detection for airpods/headsets using color and position
    This is a simplified approach since these items aren't in the COCO dataset
    """
    try:
        # Convert to HSV for better color detection
        hsv = cv2.cvtColor(image, cv2.COLOR_BGR2HSV)
        
        # Define range for white color (airpods are typically white)
        lower_white = np.array([0, 0, 200])
        upper_white = np.array([180, 30, 255])
        
        # Threshold the HSV image to get only white colors
        mask = cv2.inRange(hsv, lower_white, upper_white)
        
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
                
                # Approximate ear positions
                left_ear = (int(landmarks[234].x * w), int(landmarks[234].y * h))
                right_ear = (int(landmarks[454].x * w), int(landmarks[454].y * h))
                
                # Check if any white contours are near the ears
                for contour in contours:
                    area = cv2.contourArea(contour)
                    if area > 100:  # Filter out very small contours
                        M = cv2.moments(contour)
                        if M["m00"] != 0:
                            cx = int(M["m10"] / M["m00"])
                            cy = int(M["m01"] / M["m00"])
                            
                            # Check if this white object is near either ear
                            left_dist = np.sqrt((cx - left_ear[0])**2 + (cy - left_ear[1])**2)
                            right_dist = np.sqrt((cx - right_ear[0])**2 + (cy - right_ear[1])**2)
                            
                            # If the white object is close to an ear, it might be an airpod
                            if left_dist < 50 or right_dist < 50:
                                return "AIRPODS_OR_HEADSET"
        
        return None
    except Exception as e:
        print(f"Error in airpods/headset detection: {e}")
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
    
    if not img_data:
        return "ERROR", 400

    curr_path = save_image_from_base64(img_data)
    
    # Load the image for analysis
    image = cv2.imread(curr_path)
    
    # Check if a face is detected
    if detect_face(image):
        return "FACE_DETECTED"
    else:
        return "NO_FACE_DETECTED"

@app.route("/capture", methods=["POST"])
def capture():
    img_data = request.form.get("image")
    username = request.form.get("username")
    noise_violation = request.form.get("noise_violation")
    reference_face_path = request.form.get("reference_face")

    if not img_data:
        return "ERROR", 400

    curr_path = save_image_from_base64(img_data)
    
    # Load the image for analysis
    image = cv2.imread(curr_path)
    
    # Check for multiple faces in the first image as well
    if reference_face_path is None:
        if detect_multiple_faces(image):
            return "MULTIPLE_FACES"
        return "OK"

    # 1. Check for multiple faces first
    if detect_multiple_faces(image):
        return "MULTIPLE_FACES"

    # 2. Check for face mismatch with the reference face from login
    if not compare_faces(reference_face_path, curr_path):
        return "FACE_MISMATCH"

    # 3. Check for prohibited items
    prohibited_item = detect_prohibited_items(image)
    if prohibited_item:
        return f"PROHIBITED_ITEM:{prohibited_item}"

    # 4. Check for gaze violation
    if not detect_gaze(image):
        return "GAZE_VIOLATION"

    # 5. Then, check for noise violation
    if noise_violation == "true":
        return "NOISE_VIOLATION"

    # If all checks pass
    return "OK"

if __name__ == "__main__":
    app.run(port=5000)